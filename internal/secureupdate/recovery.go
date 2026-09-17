package secureupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type installedFile struct {
	Digest string `json:"sha256"`
	Size   int64  `json:"size"`
}

// recordArchive binds the installed files to authenticated archive bytes. The
// manifest lives in the verifier's root-owned state, never in the release tree.
func recordArchive(dir, digest string, data []byte) error {
	return recordArchiveReader(dir, digest, bytes.NewReader(data))
}
func recordArchiveReader(dir, digest string, data io.ReadSeeker) error {
	if e := validateArchiveReader(data); e != nil {
		return e
	}
	if _, e := data.Seek(0, io.SeekStart); e != nil {
		return e
	}
	z, e := gzip.NewReader(data)
	if e != nil {
		return e
	}
	defer z.Close()
	r := tar.NewReader(z)
	files := map[string]installedFile{}
	seen := map[string]bool{}
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		name := path.Clean(h.Name)
		if seen[name] {
			return errors.New("duplicate archive path")
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if name == "VERIFIED-SHA256" {
			return errors.New("reserved archive path")
		}
		hash := sha256.New()
		if _, e = io.Copy(hash, r); e != nil {
			return e
		}
		files[name] = installedFile{hex.EncodeToString(hash.Sum(nil)), h.Size}
	}
	if len(files) == 0 {
		return errors.New("empty release archive")
	}
	if e = os.MkdirAll(filepath.Join(dir, "installed"), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(files)
	if e != nil {
		return e
	}
	return writeState(filepath.Join(dir, "installed", digest+".json"), b)
}

// CheckInstalled hashes actual files and refuses symlink traversal and additions.
// The caller must keep the product's installation lock through activation.
func CheckInstalled(dir, digest, root string) error {
	raw, e := os.ReadFile(filepath.Join(dir, "installed", digest+".json"))
	if e != nil {
		return errors.New("缺少受信安装文件清单，请先准备安全恢复版本")
	}
	var files map[string]installedFile
	if json.Unmarshal(raw, &files) != nil || len(files) == 0 {
		return errors.New("invalid installed manifest")
	}
	seen := map[string]bool{}
	e = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("release tree contains symlink")
		}
		var owner unix.Stat_t
		if err = unix.Lstat(p, &owner); err != nil {
			return err
		}
		if int(owner.Uid) != os.Geteuid() || owner.Mode&0022 != 0 {
			return errors.New("installed release ownership or permissions unsafe")
		}
		if d.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if name == "VERIFIED-SHA256" {
			return nil
		}
		want, ok := files[name]
		if !ok {
			return errors.New("unexpected installed release file")
		}
		fd, err := unix.Open(p, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), p)
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Size() != want.Size {
			return errors.New("installed release size mismatch")
		}
		hash := sha256.New()
		if _, err = io.Copy(hash, io.LimitReader(f, want.Size+1)); err != nil {
			return err
		}
		if hex.EncodeToString(hash.Sum(nil)) != want.Digest {
			return errors.New("installed release content mismatch")
		}
		seen[name] = true
		return nil
	})
	if e != nil {
		return e
	}
	if len(seen) != len(files) {
		return errors.New("missing installed release file")
	}
	return nil
}

// Recovery authorizes a single bounded maintenance transaction. It tolerates
// policy expiry during switching, but never a newly learned revocation/floor.
// It is not an online verification receipt and cannot authorize a new update.
type Recovery struct {
	Component string    `json:"component"`
	Arch      string    `json:"arch"`
	Digest    string    `json:"digest"`
	Source    string    `json:"source"`
	Sequence  int64     `json:"policy_sequence"`
	Deadline  time.Time `json:"deadline"`
}

func PrepareRecovery(dir string, p Policy, component, arch, digest, source string) (Recovery, error) {
	r := Recovery{component, arch, strings.ToLower(digest), source, 0, time.Now().Add(30 * time.Minute)}
	if p.ChecksumOnly {
		r.Sequence = 1
		if component == "controller" {
			if e := CheckRollback(dir, p, component, arch, r.Digest); e != nil {
				return Recovery{}, e
			}
		}
		if e := r.checkBytes(dir); e != nil {
			return Recovery{}, e
		}
		return r, nil
	}
	if e := CheckRollback(dir, p, component, arch, r.Digest); e != nil {
		return Recovery{}, e
	}
	rp, e := readReleasePolicy(dir)
	if e != nil {
		return Recovery{}, e
	}
	r.Sequence = rp.Sequence
	if e = r.checkBytes(dir); e != nil {
		return Recovery{}, e
	}
	return r, nil
}
func (r Recovery) checkBytes(dir string) error {
	if r.Component == "controller" {
		return CheckInstalled(dir, r.Digest, r.Source)
	}
	if r.Component != "agent" {
		return errors.New("unsupported recovery component")
	}
	fd, e := unix.Open(r.Source, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), r.Source)
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Size() > 128<<20 {
		return errors.New("invalid recovery binary")
	}
	h := sha256.New()
	if _, e = io.Copy(h, io.LimitReader(f, 128<<20+1)); e != nil {
		return e
	}
	if hex.EncodeToString(h.Sum(nil)) != r.Digest {
		return errors.New("recovery binary changed")
	}
	return nil
}
func (r Recovery) Check(dir string, p Policy) error {
	if !r.Deadline.After(time.Now()) || r.Deadline.After(time.Now().Add(30*time.Minute)) || r.Sequence < 1 {
		return errors.New("恢复事务授权已过期或无效")
	}
	if p.ChecksumOnly {
		return r.checkBytes(dir)
	}
	rp, e := readReleasePolicy(dir)
	if e != nil {
		return e
	}
	if rp.Sequence < r.Sequence {
		return errors.New("recovery policy rolled back")
	}
	if e = checkRollback(dir, p, r.Component, r.Arch, r.Digest, true); e != nil {
		return e
	}
	return r.checkBytes(dir)
}
func PrepareAgentRecovery(source string) (Recovery, error) {
	p, e := LoadPolicy()
	if e != nil {
		return Recovery{}, e
	}
	fd, e := unix.Open(source, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return Recovery{}, e
	}
	f := os.NewFile(uintptr(fd), source)
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return Recovery{}, e
	}
	if !st.Mode().IsRegular() || st.Size() > 128<<20 {
		return Recovery{}, errors.New("invalid recovery binary")
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, 128<<20+1))
	if e != nil {
		return Recovery{}, e
	}
	if n > 128<<20 {
		return Recovery{}, errors.New("recovery binary exceeds limit")
	}
	return PrepareRecovery(StateDir, p, "agent", runtime.GOARCH, hex.EncodeToString(h.Sum(nil)), source)
}
func (r Recovery) CheckLocal() error {
	p, e := LoadPolicy()
	if e != nil {
		return e
	}
	return r.Check(StateDir, p)
}

func recoveryEntry(args []string) (bool, error) {
	if len(args) == 0 || (args[0] != "prepare-recovery" && args[0] != "check-recovery") {
		return false, nil
	}
	p, e := LoadPolicy()
	if e != nil {
		return true, e
	}
	if args[0] == "prepare-recovery" && len(args) == 5 {
		r, e := PrepareRecovery(StateDir, p, args[1], runtime.GOARCH, args[2], args[3])
		if e != nil {
			return true, e
		}
		if e = protectedParents(filepath.Dir(args[4])); e != nil {
			return true, e
		}
		b, e := json.Marshal(r)
		if e != nil {
			return true, e
		}
		return true, writeState(args[4], b)
	}
	if args[0] == "check-recovery" && len(args) == 2 {
		b, e := protected(args[1])
		if e != nil {
			return true, e
		}
		var r Recovery
		if e = json.Unmarshal(b, &r); e != nil {
			return true, e
		}
		return true, r.Check(StateDir, p)
	}
	return true, errors.New("invalid recovery command")
}
