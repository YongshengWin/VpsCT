package agent

import (
	"context"
	"crypto/sha256"
	"ctlvps/internal/agentwork"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/diskbudget"
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

var verifyRelease = func(ctx context.Context, component, version string, data io.ReadSeeker) error {
	if err := secureupdate.Allow("agent.update"); err != nil {
		return err
	}
	return secureupdate.VerifyReader(ctx, component, version, data)
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
	if !agentwork.Available() {
		return applySelfUpdateInline(ctx, baseURL, spec)
	}
	b, err := json.Marshal(updateRequest{baseURL, spec})
	if err != nil {
		return err
	}
	return agentwork.Start(ctx, "agent-update", b)
}

type updateRequest struct {
	BaseURL string
	Spec    agentproto.AgentUpdateSpec
}

func UpdateEntry(args []string) (bool, error) {
	if len(args) != 1 || args[0] != "agent-update" {
		return false, nil
	}
	if os.Geteuid() != 0 {
		return true, errors.New("resource worker requires root")
	}
	b, err := safehttp.ReadBounded(os.Stdin, 64<<10)
	if err != nil {
		return true, err
	}
	var r updateRequest
	if err = json.Unmarshal(b, &r); err != nil {
		return true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	return true, applySelfUpdateInline(ctx, r.BaseURL, r.Spec)
}
func applySelfUpdateInline(ctx context.Context, baseURL string, spec agentproto.AgentUpdateSpec) error {
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
	if err = diskbudget.Check(filepath.Dir(target), 128<<20, 2); err != nil {
		return err
	}
	data, err := os.CreateTemp(filepath.Dir(target), ".agent-update-")
	if err != nil {
		return err
	}
	defer os.Remove(data.Name())
	defer data.Close()
	if _, err = downloadSelf(ctx, rawURL, data); err != nil {
		return err
	}
	got, err := core.FileDigest(data)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, spec.SHA256) {
		return fmt.Errorf("sha256 mismatch")
	}
	if err = verifyRelease(ctx, "agent", "", data); err != nil {
		return err
	}
	return core.CommitBinary(data, target)
}

var downloadSelf = func(ctx context.Context, raw string, dst io.Writer) (int64, error) {
	return agentnet.DownloadTo(ctx, raw, 128<<20, true, dst)
}
