// Package agentnet moves untrusted HTTP/TLS parsing out of the root coordinator.
// The child has no control channel to root: only one bounded HTTP response.
package agentnet

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"ctlvps/internal/agentbudget"
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
	gate := controlSlot
	if r.URL.Path == "/api/agent/v1/connlog" {
		gate = logSlot
	}
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	default:
		return nil, errors.New("agent network slot busy")
	}
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
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "GOMAXPROCS=1", "GOMEMLIMIT=" + agentbudget.NetworkGoLimit}
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
	// Keep the existing wire format so old privileged callers can use a
	// newly installed helper. Decode directly from the bounded pipe, without
	// retaining a separate complete JSON/base64 byte slice.
	limited := &io.LimitedReader{R: out, N: 12<<20 + 1}
	decoder := json.NewDecoder(limited)
	var result response
	readErr := decoder.Decode(&result)
	if readErr == nil {
		var extra struct{}
		if err := decoder.Decode(&extra); err != io.EOF {
			readErr = errors.New("隔离网络响应包含额外数据")
		}
		if limited.N == 0 || len(result.Body) > 8<<20 {
			readErr = safehttp.ErrSize
		}
	}
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
	status := result.Status
	if status < 100 || status > 599 {
		return nil, errors.New("隔离网络响应无效")
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(result.Body)), Request: r}, nil
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
	limit := int64(256 << 10)
	if in.Public {
		limit = 8 << 20
	} else if u.Path == "/api/agent/v1/desired" {
		limit = agentbudget.ConfigBytes
	}
	return true, writeResponse(os.Stdout, resp.StatusCode, &limitedBody{Reader: resp.Body, remaining: limit})
}

// Stream the legacy JSON envelope, including base64, without an artifact-sized
// body or encoding buffer in the unprivileged helper.
func writeResponse(dst io.Writer, status int, body io.Reader) error {
	if _, err := fmt.Fprintf(dst, `{"Status":%d,"Body":"`, status); err != nil {
		return err
	}
	encoded := base64.NewEncoder(base64.StdEncoding, dst)
	if _, err := safehttp.CopyBounded(encoded, body, 8<<20); err != nil {
		return err
	}
	if err := encoded.Close(); err != nil {
		return err
	}
	_, err := io.WriteString(dst, "\"}\n")
	return err
}

var controlSlot = make(chan struct{}, 1)
var logSlot = make(chan struct{}, 1)

// Unlike LimitReader this reports excess instead of accepting truncated JSON.
type limitedBody struct {
	io.Reader
	remaining int64
}

func (r *limitedBody) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var one [1]byte
		n, err := r.Reader.Read(one[:])
		if n > 0 {
			return 0, safehttp.ErrSize
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.Reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}
