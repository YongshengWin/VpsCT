package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/agentproto"
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

// download fetches url into memory (cores are < 60 MB) with a size cap.
func download(ctx context.Context, url string) ([]byte, error) {
	return agentnet.Download(ctx, url, 200<<20, false)
}

// verify checks the archive hash when one is pinned for this arch.
func verify(data []byte, v agentproto.CoreVersion) error {
	want := v.SHA256[runtime.GOARCH]
	if want == "" {
		return errors.New("缺少内核 SHA256，拒绝安装")
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimSpace(want)) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", hex.EncodeToString(sum[:]), want)
	}
	return nil
}

// extractBinary finds a file named `name` inside a tar.gz or zip archive (or
// returns data itself when it is a raw ELF binary).
func extractBinary(data []byte, name string) ([]byte, error) {
	if len(data) > 4 && bytes.Equal(data[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		return data, nil
	}
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		tr := tar.NewReader(io.LimitReader(gz, 256<<20))
		entries := 0
		for {
			entries++
			if entries > 4096 {
				return nil, errors.New("too many archive entries")
			}
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name && !strings.Contains(h.Name, "..") && !filepath.IsAbs(h.Name) {
				return safehttp.ReadBounded(tr, 200<<20)
			}
		}
		return nil, fmt.Errorf("%s not found in tar.gz", name)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("unknown archive format")
	}
	if len(zr.File) > 4096 {
		return nil, errors.New("too many archive entries")
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == name && f.Mode().IsRegular() && !strings.Contains(f.Name, "..") && !filepath.IsAbs(f.Name) {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return safehttp.ReadBounded(rc, 200<<20)
		}
	}
	return nil, fmt.Errorf("%s not found in zip", name)
}

// installBinary downloads, verifies and atomically installs name into binDir.
func installBinary(ctx context.Context, binDir, name string, v agentproto.CoreVersion) error {
	if v.Version == "" || v.URL == "" {
		return fmt.Errorf("no version pinned for %s", name)
	}
	if e := secureupdate.Allow("core.install"); e != nil {
		return e
	}
	url := ExpandURL(v.URL, v.Version)
	policy, e := secureupdate.LoadPolicy()
	if e != nil {
		return e
	}
	if policy.ChecksumOnly {
		var official string
		switch name {
		case "sing-box":
			official = fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/v%s/sing-box-%s-linux-%s.tar.gz", v.Version, v.Version, archFor("arch"))
		case "snell-server":
			official = fmt.Sprintf("https://dl.nssurge.com/snell/snell-server-v%s-linux-%s.zip", v.Version, archFor("snellarch"))
		default:
			return errors.New("unknown core")
		}
		if strings.ContainsAny(v.Version, "/\\?#%") || url != official {
			return errors.New("core URL must match the fixed upstream release")
		}
	}
	data, err := download(ctx, url)
	if err != nil {
		return err
	}
	if !policy.ChecksumOnly || v.SHA256[runtime.GOARCH] != "" {
		if err := verify(data, v); err != nil {
			return err
		}
	}
	if !policy.ChecksumOnly {
		if err := secureupdate.Verify(ctx, name, v.Version, data); err != nil {
			return err
		}
	}
	bin, err := extractBinary(data, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(binDir, name)
	if _, err := WriteIfChanged(target, bin, 0755); err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	receipt, _ := json.Marshal(struct {
		Version string
		SHA256  string
	}{v.Version, hex.EncodeToString(sum[:])})
	_, err = WriteIfChanged(target+".trusted", receipt, 0600)
	if err != nil {
		return err
	}
	return nil
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
