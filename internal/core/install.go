package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ctlvps-agent")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// verify checks the archive hash when one is pinned for this arch.
func verify(data []byte, v agentproto.CoreVersion) error {
	want := v.SHA256[runtime.GOARCH]
	if want == "" {
		return nil
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
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
				return io.ReadAll(tr)
			}
		}
		return nil, fmt.Errorf("%s not found in tar.gz", name)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("unknown archive format")
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == name && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("%s not found in zip", name)
}

// installedVersion runs `bin version|-v` and extracts the version token.
func installedVersion(ctx context.Context, bin string, args ...string) string {
	if _, err := os.Stat(bin); err != nil {
		return ""
	}
	out, _ := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// installBinary downloads, verifies and atomically installs name into binDir.
func installBinary(ctx context.Context, binDir, name string, v agentproto.CoreVersion) error {
	if v.Version == "" || v.URL == "" {
		return fmt.Errorf("no version pinned for %s", name)
	}
	url := ExpandURL(v.URL, v.Version)
	data, err := download(ctx, url)
	if err != nil {
		return err
	}
	if err := verify(data, v); err != nil {
		return err
	}
	bin, err := extractBinary(data, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(binDir, name)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	// remember which version the file is, since some cores lack `version`
	_ = os.WriteFile(target+".version", []byte(v.Version), 0o644)
	return nil
}

// recordedVersion reads the sidecar written by installBinary.
func recordedVersion(bin string) string {
	b, err := os.ReadFile(bin + ".version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
