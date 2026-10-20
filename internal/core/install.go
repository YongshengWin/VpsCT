package core

import (
	"archive/tar"
	"archive/zip"

	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/boundedexec"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/diskbudget"
	"ctlvps/internal/safehttp"
	"ctlvps/internal/secureupdate"
)

// archFor maps GOARCH to release naming.
func archFor(kind string) string {
	switch kind {
	case "snellarch":
		if runtime.GOARCH == "arm64" {
			return "aarch64"
		}
		return runtime.GOARCH
	}
	return runtime.GOARCH
}

// ExpandURL fills {version} {arch} {snellarch}.
func ExpandURL(tmpl, version string) string {
	r := strings.NewReplacer("{version}", version, "{arch}", archFor("arch"), "{snellarch}", archFor("snellarch"))
	return r.Replace(tmpl)
}

func officialSingBoxArchive(version, arch string, musl bool) string {
	suffix := ""
	if musl {
		suffix = "-musl"
	}
	return fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/v%s/sing-box-%s-linux-%s%s.tar.gz", version, version, arch, suffix)
}

// Alpine selects the upstream musl build locally; the controller cannot choose
// another publisher, version or architecture by supplying an alternate URL.
func selectSingBoxArchive(raw, version, arch string, alpine bool) (string, error) {
	if !corecompat.ReleaseVersion(version) || (arch != "amd64" && arch != "arm64") {
		return "", errors.New("unsupported official sing-box release or architecture")
	}
	standard := officialSingBoxArchive(version, arch, false)
	selected := officialSingBoxArchive(version, arch, alpine)
	if raw != standard && raw != selected {
		return "", errors.New("sing-box must use an official release and its fixed upstream archive URL")
	}
	return selected, nil
}

func platformCoreChecksum(name, arch string, alpine bool, sums map[string]string) string {
	if name == "sing-box" && alpine && strings.TrimSpace(sums[arch+"-musl"]) != "" {
		return strings.TrimSpace(sums[arch+"-musl"])
	}
	return strings.TrimSpace(sums[arch])
}

// ExtractBinary streams a selected archive member into a private staging
// file. Compressed input, total expansion, entry count and output are bounded.
func extractBinaryTo(data *os.File, name string, dst io.Writer) error {
	return extractBinaryToBudget(data, name, dst, nil)
}

// Check the selected member before writing it, with the downloaded archive
// already accounted for in filesystem free space.
func extractBinaryToBudget(data *os.File, name string, dst io.Writer, check func(int64) error) error {
	budget := func(size int64) error {
		if size < 0 || size > 200<<20 {
			return safehttp.ErrSize
		}
		if check != nil {
			return check(size)
		}
		return nil
	}
	if _, err := data.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var magic [4]byte
	if _, err := io.ReadFull(data, magic[:]); err != nil {
		return err
	}
	if _, err := data.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if string(magic[:]) == "\x7fELF" {
		st, err := data.Stat()
		if err != nil {
			return err
		}
		if err := budget(st.Size()); err != nil {
			return err
		}
		_, err = safehttp.CopyBounded(dst, data, 200<<20)
		return err
	}
	if magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(data)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(io.LimitReader(gz, 256<<20+1))
		var total int64
		for entries := 0; entries < 4096; entries++ {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if h.Size < 0 || h.Size > 256<<20-total {
				return errors.New("archive expansion exceeds limit")
			}
			total += h.Size
			if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name && !strings.Contains(h.Name, "..") && !filepath.IsAbs(h.Name) {
				if err := budget(h.Size); err != nil {
					return err
				}
				_, err = safehttp.CopyBounded(dst, tr, 200<<20)
				return err
			}
		}
		return fmt.Errorf("%s missing or archive entry limit exceeded", name)
	}
	st, err := data.Stat()
	if err != nil {
		return err
	}
	if err = checkZipDirectory(data, st.Size()); err != nil {
		return err
	}
	zr, err := zip.NewReader(data, st.Size())
	if err != nil {
		return errors.New("unknown archive format")
	}
	if len(zr.File) > 4096 {
		return errors.New("too many archive entries")
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == name && f.Mode().IsRegular() && !strings.Contains(f.Name, "..") && !filepath.IsAbs(f.Name) {
			if f.UncompressedSize64 > 200<<20 {
				return safehttp.ErrSize
			}
			if err := budget(int64(f.UncompressedSize64)); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			_, err = safehttp.CopyBounded(dst, rc, 200<<20)
			return err
		}
	}
	return fmt.Errorf("%s not found in zip", name)
}

// Bound ZIP metadata before archive/zip allocates names and entry objects.
// Release artifacts do not need multi-disk or ZIP64 archives.
func checkZipDirectory(f *os.File, size int64) error {
	tail := make([]byte, min(size, 65557))
	if _, err := f.ReadAt(tail, size-int64(len(tail))); err != nil {
		return err
	}
	for i := len(tail) - 22; i >= 0; i-- {
		if !bytes.Equal(tail[i:i+4], []byte{'P', 'K', 5, 6}) {
			continue
		}
		e := tail[i:]
		if i+22+int(binary.LittleEndian.Uint16(e[20:])) != len(tail) {
			continue
		}
		entries := binary.LittleEndian.Uint16(e[10:])
		if binary.LittleEndian.Uint16(e[4:]) != 0 || binary.LittleEndian.Uint16(e[6:]) != 0 || binary.LittleEndian.Uint16(e[8:]) != entries || entries > 4096 || binary.LittleEndian.Uint32(e[12:]) > 2<<20 || binary.LittleEndian.Uint32(e[16:]) == 0xffffffff {
			return errors.New("ZIP metadata exceeds budget or unsupported format")
		}
		directorySize := int64(binary.LittleEndian.Uint32(e[12:]))
		directoryOffset := int64(binary.LittleEndian.Uint32(e[16:]))
		endOffset := size - int64(len(tail)) + int64(i)
		if directoryOffset+directorySize != endOffset || (i >= 20 && bytes.Equal(tail[i-20:i-16], []byte{'P', 'K', 6, 7})) {
			return errors.New("unsupported ZIP layout")
		}
		// Verify actual records, not just the untrusted EOCD count. Go's ZIP
		// reader permits a wrapped 16-bit count and reads until a non-header.
		r := io.NewSectionReader(f, directoryOffset, directorySize)
		var header [46]byte
		remaining := directorySize
		count := 0
		for remaining > 0 {
			if count >= 4096 || remaining < 46 {
				return errors.New("ZIP directory record budget exceeded")
			}
			if _, err := io.ReadFull(r, header[:]); err != nil {
				return err
			}
			if !bytes.Equal(header[:4], []byte{'P', 'K', 1, 2}) {
				return errors.New("invalid ZIP directory record")
			}
			extra := int64(binary.LittleEndian.Uint16(header[28:])) + int64(binary.LittleEndian.Uint16(header[30:])) + int64(binary.LittleEndian.Uint16(header[32:]))
			remaining -= 46
			if extra > remaining {
				return errors.New("truncated ZIP metadata")
			}
			if _, err := r.Seek(extra, io.SeekCurrent); err != nil {
				return err
			}
			remaining -= extra
			count++
		}
		if count != int(entries) {
			return errors.New("ZIP directory count mismatch")
		}
		return nil
	}
	return errors.New("invalid ZIP directory")
}

// CommitBinary activates a verified private staging file in the same directory.
// It never reads the old or new executable into memory.
func CommitBinary(f *os.File, target string) error {
	if st, err := os.Lstat(target); err == nil && !st.Mode().IsRegular() {
		return errors.New("target is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if filepath.Dir(f.Name()) != filepath.Dir(target) {
		return errors.New("staging must share target directory")
	}
	if err := f.Chmod(0755); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), target); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// FileDigest hashes a bounded artifact without retaining its contents.
func FileDigest(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := safehttp.CopyBounded(h, f, 256<<20); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// installBinary downloads, verifies and atomically installs name into binDir.
func installBinaryInline(ctx context.Context, binDir, name string, v agentproto.CoreVersion) (installErr error) {
	if name == "mita" && v.SHA256[runtime.GOARCH] == "" {
		return errors.New("mita 锁定版本需要当前架构的 SHA-256")
	}
	if v.Version == "" || v.URL == "" {
		return fmt.Errorf("no version pinned for %s", name)
	}
	if e := secureupdate.Allow("core.install"); e != nil {
		return e
	}
	url := ExpandURL(v.URL, v.Version)
	_, alpineErr := os.Stat("/etc/alpine-release")
	alpine := alpineErr == nil
	wantChecksum := platformCoreChecksum(name, runtime.GOARCH, alpine, v.SHA256)
	if name == "sing-box" {
		var err error
		url, err = selectSingBoxArchive(url, v.Version, archFor("arch"), alpine)
		if err != nil {
			return err
		}
	}
	policy, e := secureupdate.LoadPolicy()
	if e != nil {
		return e
	}
	if policy.ChecksumOnly {
		var official string
		switch name {
		case "sing-box":
			// Already selected and validated above using this host's libc.
			official = url
		case "snell-server":
			official = fmt.Sprintf("https://dl.nssurge.com/snell/snell-server-v%s-linux-%s.zip", v.Version, archFor("snellarch"))
		case "mita":
			official = fmt.Sprintf("https://github.com/enfein/mieru/releases/download/v%s/mita_%s_linux_%s.tar.gz", v.Version, v.Version, archFor("arch"))
		default:
			return errors.New("unknown core")
		}
		if strings.ContainsAny(v.Version, "/\\?#%") || url != official {
			return errors.New("core URL must match the fixed upstream release")
		}
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}
	if err := diskbudget.Check(binDir, 200<<20, 4); err != nil {
		return err
	}
	data, err := os.CreateTemp(binDir, ".core-archive-")
	if err != nil {
		return err
	}
	defer os.Remove(data.Name())
	defer data.Close()
	if _, err = agentnet.DownloadTo(ctx, url, 200<<20, false, data); err != nil {
		return err
	}
	if !policy.ChecksumOnly || wantChecksum != "" {
		got, e := FileDigest(data)
		if e != nil {
			return e
		}
		if wantChecksum == "" || !strings.EqualFold(got, wantChecksum) {
			return errors.New("core SHA256 mismatch")
		}
	}
	if !policy.ChecksumOnly {
		if err = secureupdate.VerifyReader(ctx, name, v.Version, data); err != nil {
			return err
		}
	}
	bin, err := os.CreateTemp(binDir, ".core-binary-")
	if err != nil {
		return err
	}
	defer os.Remove(bin.Name())
	defer bin.Close()
	if err = extractBinaryToBudget(data, name, bin, func(size int64) error {
		return diskbudget.Check(binDir, size, 4)
	}); err != nil {
		return err
	}
	// The verified archive is no longer needed. Release its blocks before
	// reserving the existing executable and metadata for rollback.
	if err = data.Close(); err != nil {
		return err
	}
	if err = os.Remove(data.Name()); err != nil {
		return err
	}
	digest, err := FileDigest(bin)
	if err != nil {
		return err
	}
	if name == "sing-box" {
		if err = bin.Chmod(0755); err != nil {
			return err
		}
		// Linux refuses to execute a file still open for writing (ETXTBSY).
		// Close the private candidate before probing it, then reopen read-only
		// for the atomic activation helper.
		if err = bin.Sync(); err != nil {
			return err
		}
		if err = bin.Close(); err != nil {
			return err
		}
		check, cancel := context.WithTimeout(ctx, 15*time.Second)
		out, _, err := boundedexec.Run(check, "", 64<<10, bin.Name(), "version")
		cancel()
		if err != nil {
			return fmt.Errorf("official sing-box candidate cannot run: %w", err)
		}
		if !strings.Contains(string(out), "sing-box version "+v.Version+"\n") {
			return errors.New("official sing-box candidate reports a different version")
		}
		bin, err = os.Open(bin.Name())
		if err != nil {
			return err
		}
		defer bin.Close()
	}
	target := filepath.Join(binDir, name)
	recovery, err := prepareBinaryRecovery(target)
	if err != nil {
		return err
	}
	defer func() {
		if installErr != nil {
			installErr = rollbackCoreAfterFailure(target, nil, recovery.OldPolicy, installErr)
		}
	}()
	// Persist activation intent before replacing the executable. The worker
	// may finish between coordinator ticks, or the coordinator may restart.
	if err = markActivation(target); err != nil {
		return err
	}
	if err = CommitBinary(bin, target); err != nil {
		return err
	}
	receipt, _ := json.Marshal(struct {
		Version string
		SHA256  string
	}{v.Version, digest})
	_, err = WriteIfChanged(target+".trusted", receipt, 0600)
	if err != nil {
		return err
	}
	recovery.Phase = "installed"
	if err = syncDirectory(binDir); err != nil {
		return err
	}
	return writeCoreRecovery(target, recovery)
}

// recordedVersion reads the sidecar written by installBinary.
func recordedVersion(bin string) string {
	b, e := os.ReadFile(bin + ".trusted")
	if e != nil {
		return ""
	}
	var r struct {
		Version string
		SHA256  string
	}
	if json.Unmarshal(b, &r) != nil {
		return ""
	}
	f, e := os.Open(bin)
	if e != nil {
		return ""
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() > 200<<20 {
		return ""
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return ""
	}
	if hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return ""
	}
	return r.Version
}
