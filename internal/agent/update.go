package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/safehttp"
	"ctlvps/internal/secureupdate"
	"net/url"
)

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var verifyRelease = func(ctx context.Context, component, version string, data []byte) error {
	if err := secureupdate.Allow("agent.update"); err != nil {
		return err
	}
	return secureupdate.Verify(ctx, component, version, data)
}

var executablePath = func() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func resolveUpdateURL(base, u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(u, "/")
}

func applySelfUpdate(ctx context.Context, baseURL string, spec agentproto.AgentUpdateSpec) error {
	if spec.SHA256 == "" || spec.URL == "" {
		return fmt.Errorf("incomplete update spec")
	}
	target := executablePath()
	if target == "" {
		return fmt.Errorf("cannot resolve executable path")
	}
	rawURL := resolveUpdateURL(baseURL, spec.URL)
	u, err := url.Parse(rawURL)
	base, baseErr := url.Parse(baseURL)
	if err != nil || baseErr != nil || u.Scheme != "https" || u.User != nil || safehttp.Origin(u) != safehttp.Origin(base) {
		return fmt.Errorf("invalid update origin")
	}
	data, err := downloadSelf(ctx, rawURL)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, spec.SHA256) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, spec.SHA256)
	}
	if err := verifyRelease(ctx, "agent", "", data); err != nil {
		return err
	}
	_, err = core.WriteIfChanged(target, data, 0755)
	return err
}

var downloadSelf = func(ctx context.Context, raw string) ([]byte, error) {
	return agentnet.Download(ctx, raw, 128<<20, true)
}
