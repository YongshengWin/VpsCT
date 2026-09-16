package agentnet

import (
	"bytes"
	"context"
	"ctlvps/internal/safehttp"
	"encoding/json"
	"errors"
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

func Download(ctx context.Context, raw string, limit int64, controller bool) ([]byte, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return fetch(ctx, downloadRequest{raw, limit, controller})
	}
	exe := networkExecutable()
	cmd := exec.CommandContext(ctx, exe, "network-download")
	b, _ := json.Marshal(downloadRequest{raw, limit, controller})
	cmd.Stdin = bytes.NewReader(b)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	if err := isolate(cmd); err != nil {
		return nil, err
	}
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	data, e := safehttp.ReadBounded(pipe, limit)
	if e != nil {
		_ = cmd.Process.Kill()
	}
	wait := cmd.Wait()
	if e != nil {
		return nil, e
	}
	if wait != nil {
		return nil, errors.New("隔离下载失败")
	}
	return data, nil
}
func fetch(ctx context.Context, in downloadRequest) ([]byte, error) {
	if in.Limit < 1 || in.Limit > 256<<20 {
		return nil, errors.New("invalid download budget")
	}
	c := safehttp.New(safehttp.Options{})
	if in.Controller {
		u, e := url.Parse(in.URL)
		if e != nil || u.Scheme != "https" || u.User != nil || !strings.HasPrefix(u.Path, "/dl/agent/linux-") {
			return nil, errors.New("invalid controller download")
		}
		c = &http.Client{Timeout: 3 * time.Minute, Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	r, e := http.NewRequestWithContext(ctx, "GET", in.URL, nil)
	if e != nil {
		return nil, e
	}
	resp, e := c.Do(r)
	if e != nil {
		return nil, errors.New("下载连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("下载响应失败")
	}
	return safehttp.ReadBounded(resp.Body, in.Limit)
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
	data, e := fetch(ctx, in)
	if e != nil {
		return true, e
	}
	_, e = os.Stdout.Write(data)
	return true, e
}

func networkExecutable() string {
	p, _ := os.Executable()
	// Maintenance copies are private to root; use the independently provisioned helper.
	if strings.Contains(p, "/ctlvps-maintenance/") {
		return "/usr/local/libexec/ctlvps-verify"
	}
	return p
}
