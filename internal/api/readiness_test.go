package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestControllerReadinessRejectsProxyAccess(t *testing.T) {
	c := newTestAPI(t)
	for _, trusted := range []bool{false, true} {
		if trusted {
			c.api.Config.TrustedProxyCIDRs = []string{"127.0.0.0/8"}
		}
		for _, header := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "CF-Connecting-IP"} {
			r := httptest.NewRequest(http.MethodGet, "/healthz?ready=1", nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set(header, "203.0.113.1")
			w := httptest.NewRecorder()
			c.api.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("trusted=%v header=%s: status=%d", trusted, header, w.Code)
			}
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/healthz?ready=1", nil)
	r.RemoteAddr = "203.0.113.1:1234"
	w := httptest.NewRecorder()
	c.api.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("remote readiness status=%d", w.Code)
	}
}

func TestControllerReadinessChecksDistributionWithoutChangingLiveness(t *testing.T) {
	c := newTestAPI(t)
	c.do("GET", "/healthz?ready=1", nil, 200)
	dir := t.TempDir()
	c.api.Config.AgentBinDir = dir
	c.do("GET", "/healthz", nil, 200)
	c.do("GET", "/healthz?ready=1", nil, 503)
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(dir, "ctlvps-agent-linux-"+arch), []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	c.do("GET", "/healthz?ready=1", nil, 200)
	if err := c.api.Store.Close(); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/healthz", nil, 200)
	c.do("GET", "/healthz?ready=1", nil, 503)
}
