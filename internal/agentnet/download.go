package agentnet

import (
	"bytes"
	"context"
	"ctlvps/internal/agentbudget"
	"ctlvps/internal/safehttp"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type downloadRequest struct {
	URL        string
	Limit      int64
	Controller bool
}

// Download is for small metadata only. Artifacts use DownloadTo.
func Download(ctx context.Context, raw string, limit int64, controller bool) ([]byte, error) {
	if limit < 1 || limit > 8<<20 {
		return nil, errors.New("in-memory download budget exceeds metadata limit")
	}
	var b bytes.Buffer
	_, err := DownloadTo(ctx, raw, limit, controller, &b)
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// DownloadTo bounds both sides of the isolated pipe. Neither process retains
// a complete artifact; only the privileged caller can open the destination.
func DownloadTo(ctx context.Context, raw string, limit int64, controller bool, dst io.Writer) (int64, error) {
	if limit < 1 || limit > 256<<20 {
		return 0, errors.New("invalid download budget")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return fetchTo(ctx, downloadRequest{raw, limit, controller}, dst)
	}
	exe := networkExecutable()
	cmd := exec.CommandContext(ctx, exe, "network-download")
	b, _ := json.Marshal(downloadRequest{raw, limit, controller})
	cmd.Stdin = bytes.NewReader(b)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "GOMAXPROCS=1", "GOMEMLIMIT=" + agentbudget.NetworkGoLimit}
	if err := isolate(cmd); err != nil {
		return 0, err
	}
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		return 0, e
	}
	if e = cmd.Start(); e != nil {
		return 0, e
	}
	n, e := safehttp.CopyBounded(dst, pipe, limit)
	if e != nil {
		_ = cmd.Process.Kill()
	}
	wait := cmd.Wait()
	if e != nil {
		return 0, e
	}
	if wait != nil {
		return 0, errors.New("隔离下载失败")
	}
	return n, nil
}
func fetchTo(ctx context.Context, in downloadRequest, dst io.Writer) (int64, error) {
	if in.Limit < 1 || in.Limit > 256<<20 {
		return 0, errors.New("invalid download budget")
	}
	c := safehttp.New(safehttp.Options{})
	if in.Controller {
		u, e := url.Parse(in.URL)
		if e != nil || u.Scheme != "https" || u.User != nil || !(strings.HasPrefix(u.Path, "/dl/agent/linux-") || u.Path == "/dl/core/sing-box/linux-amd64" || u.Path == "/dl/core/sing-box/linux-arm64") {
			return 0, errors.New("invalid controller download")
		}
		c = &http.Client{Timeout: 3 * time.Minute, Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	r, e := http.NewRequestWithContext(ctx, "GET", in.URL, nil)
	if e != nil {
		return 0, e
	}
	resp, e := c.Do(r)
	if e != nil {
		return 0, errors.New("下载连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, errors.New("下载响应失败")
	}
	if resp.ContentLength > in.Limit {
		return 0, safehttp.ErrSize
	}
	return safehttp.CopyBounded(dst, resp.Body, in.Limit)
}
func DownloadEntry(args []string) (bool, error) {
	if len(args) != 1 || args[0] != "network-download" {
		return false, nil
	}
	if os.Geteuid() == 0 {
		return true, errors.New("network process must not run as root")
	}
	if e := harden(); e != nil {
		return true, e
	}
	b, e := safehttp.ReadBounded(os.Stdin, 16<<10)
	if e != nil {
		return true, e
	}
	var in downloadRequest
	if e = json.Unmarshal(b, &in); e != nil {
		return true, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, e = fetchTo(ctx, in, os.Stdout)
	return true, e
}

func networkExecutable() string {
	p, _ := os.Executable()
	// Maintenance and installer copies are private to root. Optional signed
	// deployments already provision an accessible, trusted network helper.
	if strings.Contains(p, "/ctlvps-maintenance/") || strings.HasPrefix(p, "/opt/ctlvps-install.") {
		return "/usr/local/libexec/ctlvps-verify"
	}
	return p
}
