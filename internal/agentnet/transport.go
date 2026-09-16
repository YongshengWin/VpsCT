// Package agentnet moves untrusted HTTP/TLS parsing out of the root coordinator.
// The child has no control channel to root: only one bounded HTTP response.
package agentnet

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"ctlvps/internal/safehttp"
)

type request struct {
	Public        bool
	PrivateOrigin bool
	URL           string
	Method        string
	Token         string
	Encoding      string
	Body          []byte
}
type response struct {
	Status int
	Body   []byte
}
type Transport struct {
	Executable    string
	Public        bool
	PrivateOrigin bool
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	var err error
	if r.Body != nil {
		body, err = safehttp.ReadBounded(r.Body, 2<<20)
		r.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	b, err := json.Marshal(request{t.Public, t.PrivateOrigin, r.URL.String(), r.Method, r.Header.Get("Authorization"), r.Header.Get("Content-Encoding"), body})
	if err != nil {
		return nil, err
	}
	exe := t.Executable
	if exe == "" {
		exe = networkExecutable()
	}
	cmd := exec.CommandContext(r.Context(), exe, "network-request")
	cmd.Stdin = bytes.NewReader(b)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	if err := isolate(cmd); err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	raw, readErr := safehttp.ReadBounded(out, 12<<20)
	if readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, errors.New("隔离网络请求失败")
	}
	var result response
	if err = json.Unmarshal(raw, &result); err != nil || result.Status < 100 || result.Status > 599 {
		return nil, errors.New("隔离网络响应无效")
	}
	return &http.Response{StatusCode: result.Status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(result.Body)), Request: r}, nil
}
func Entry(args []string) (bool, error) {
	if ok, e := DownloadEntry(args); ok {
		return ok, e
	}
	if len(args) != 1 || args[0] != "network-request" {
		return false, nil
	}
	if os.Geteuid() == 0 {
		return true, errors.New("网络子进程不得以 root 运行")
	}
	if e := harden(); e != nil {
		return true, e
	}
	b, err := safehttp.ReadBounded(os.Stdin, 3<<20)
	if err != nil {
		return true, err
	}
	var in request
	if err = json.Unmarshal(b, &in); err != nil {
		return true, err
	}
	u, err := url.Parse(in.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return true, errors.New("invalid controller URL")
	}
	allowed := map[string]string{"/api/agent/v1/enroll": "POST", "/api/agent/v1/heartbeat": "POST", "/api/agent/v1/desired": "GET", "/api/agent/v1/apply-report": "POST", "/api/agent/v1/connlog": "POST"}
	ok := allowed[u.Path] == in.Method
	if strings.HasPrefix(u.Path, "/api/maintenance/v1/jobs/") && in.Method == "POST" && (strings.HasSuffix(u.Path, "/claim") || strings.HasSuffix(u.Path, "/report")) {
		ok = true
	}
	if in.Public && in.Method == "GET" {
		ok = true
	}
	if !ok {
		return true, errors.New("unsupported agent request")
	}
	r, err := http.NewRequest(in.Method, in.URL, bytes.NewReader(in.Body))
	if err != nil {
		return true, err
	}
	r.Header.Set("Authorization", in.Token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", in.Encoding)
	c := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32 << 10}}
	if in.Public {
		opts := safehttp.Options{}
		if in.PrivateOrigin {
			opts.PrivateOrigins = []string{in.URL}
		}
		c = safehttp.New(opts)
		r.Header.Del("Authorization")
	}
	resp, err := c.Do(r)
	if err != nil {
		return true, errors.New("controller connection failed")
	}
	defer resp.Body.Close()
	data, err := safehttp.ReadBounded(resp.Body, 8<<20)
	if err != nil {
		return true, err
	}
	return true, json.NewEncoder(os.Stdout).Encode(response{resp.StatusCode, data})
}
