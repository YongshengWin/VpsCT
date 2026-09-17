package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadChecksBeforeInstall(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	content := "synthetic release bytes"
	prior := downloadAgentTo
	t.Cleanup(func() { downloadAgentTo = prior })
	downloadAgentTo = func(_ context.Context, _ string, dst io.Writer) (int64, error) {
		return io.Copy(dst, strings.NewReader(content))
	}
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(content)), Header: make(http.Header), Request: r}, nil
	})
	for _, test := range []struct {
		name, url, sha string
		ok             bool
	}{
		{"valid", "https://panel.example.test/dl/agent/linux-amd64", sha, true},
		{"tampered", "https://panel.example.test/dl/agent/linux-amd64", strings.Repeat("0", 64), false},
		{"plaintext", "http://panel.example.test/dl/agent/linux-amd64", sha, false},
		{"userinfo", "https://user:password@panel.example.test/dl/agent/linux-amd64", sha, false},
		{"bad hash", "https://panel.example.test/dl/agent/linux-amd64", "not-a-checksum", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := downloadAgent(context.Background(), test.url, test.sha, filepath.Join(t.TempDir(), "download"))
			if (err == nil) != test.ok {
				t.Fatalf("got %v", err)
			}
		})
	}
}
