package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ctlvps/internal/httpx"
)

func TestProxyHostBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, site, host, remote, path, origin string
		forwarded                              []string
		want                                   int
		effective                              string
	}{
		{name: "preserved host", host: "panel.example.com", want: 204},
		{name: "case insensitive", host: "PANEL.example.com", want: 204},
		{name: "default HTTPS port", host: "panel.example.com:443", want: 204},
		{name: "configured default port", site: "https://panel.example.com:443", host: "panel.example.com", want: 204},
		{name: "HTTP loopback default port", site: "http://localhost", host: "localhost:80", want: 204},
		{name: "custom port", site: "https://panel.example.com:8443", host: "panel.example.com:8443", want: 204},
		{name: "missing custom port", site: "https://panel.example.com:8443", host: "panel.example.com", want: 421},
		{name: "wrong port", host: "panel.example.com:80", want: 421},
		{name: "upstream host", host: "127.0.0.1:8080", want: 421},
		{name: "trusted forwarded host", host: "127.0.0.1:8080", forwarded: []string{"panel.example.com"}, want: 204, effective: "panel.example.com"},
		{name: "untrusted forwarded host", remote: "192.0.2.1:1234", host: "127.0.0.1:8080", forwarded: []string{"panel.example.com"}, want: 421},
		{name: "ignore untrusted header", remote: "192.0.2.1:1234", host: "panel.example.com", forwarded: []string{"evil.example"}, want: 204},
		{name: "forwarded mismatch", host: "panel.example.com", forwarded: []string{"evil.example"}, want: 421},
		{name: "forwarded list", host: "panel.example.com", forwarded: []string{"panel.example.com, evil.example"}, want: 421},
		{name: "duplicate forwarded values", host: "panel.example.com", forwarded: []string{"panel.example.com", "panel.example.com"}, want: 421},
		{name: "empty forwarded value", host: "panel.example.com", forwarded: []string{""}, want: 421},
		{name: "forwarded URL", host: "panel.example.com", forwarded: []string{"https://panel.example.com"}, want: 421},
		{name: "forwarded userinfo", host: "panel.example.com", forwarded: []string{"user@panel.example.com"}, want: 421},
		{name: "forwarded path", host: "panel.example.com", forwarded: []string{"panel.example.com/"}, want: 421},
		{name: "forwarded whitespace", host: "panel.example.com", forwarded: []string{" panel.example.com"}, want: 421},
		{name: "empty port", host: "panel.example.com:", want: 421},
		{name: "bracketed domain", host: "[panel.example.com]", want: 421},
		{name: "IPv6", site: "https://[::1]", host: "[::1]:443", want: 204},
		{name: "unbracketed IPv6", site: "https://[::1]", host: "::1", want: 421},
		{name: "health exemption", host: "127.0.0.1:8080", path: "/healthz", want: 204},
		{name: "forwarded write", host: "127.0.0.1:8080", forwarded: []string{"panel.example.com:443"}, origin: "https://panel.example.com", path: "/api/v1/probe", want: 204, effective: "panel.example.com:443"},
		{name: "foreign origin still rejected", host: "127.0.0.1:8080", forwarded: []string{"panel.example.com"}, origin: "https://evil.example", path: "/api/v1/probe", want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.site == "" {
				tc.site = "https://panel.example.com"
			}
			if tc.remote == "" {
				tc.remote = "127.0.0.1:1234"
			}
			if tc.path == "" {
				tc.path = "/"
			}
			if tc.effective == "" {
				tc.effective = tc.host
			}
			a := New(Deps{Config: Config{SiteURL: tc.site, TrustedProxyCIDRs: []string{"127.0.0.1/32"}}})
			method := "GET"
			if tc.origin != "" {
				method = "POST"
			}
			r := httptest.NewRequest(method, "http://backend"+tc.path, nil)
			r.Host = tc.host
			r.RemoteAddr = tc.remote
			for _, v := range tc.forwarded {
				r.Header.Add("X-Forwarded-Host", v)
			}
			r.Header.Set("Origin", tc.origin)
			// Forwarded data cannot turn an untrusted socket peer into a trusted one.
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Forwarded-Proto", "http")
			w := httptest.NewRecorder()
			a.security(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != tc.effective {
					t.Errorf("effective Host=%q want %q", r.Host, tc.effective)
				}
				if len(r.Header.Values("X-Forwarded-Host")) != 0 {
					t.Error("forwarded host leaked downstream")
				}
				w.WriteHeader(204)
			})).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == 421 && !strings.Contains(w.Body.String(), "proxy_set_header Host") {
				t.Error("missing actionable error")
			}
		})
	}
}

func TestForwardedHostIgnoredWithoutCanonicalSite(t *testing.T) {
	a := New(Deps{Config: Config{TrustedProxyCIDRs: []string{"127.0.0.1/32"}}})
	r := httptest.NewRequest("GET", "http://localhost/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-Host", "evil.example")
	w := httptest.NewRecorder()
	a.security(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "localhost" {
			t.Fatalf("Host=%q", r.Host)
		}
		w.WriteHeader(204)
	})).ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}

func TestMaintenanceHostDefaultPort(t *testing.T) {
	a := New(Deps{})
	for _, host := range []string{"panel.example.com", "panel.example.com:443", "evil.example", "panel.example.com:8443"} {
		r := httptest.NewRequest("POST", "https://panel.example.com/api/v1/probe", strings.NewReader("{"))
		r.Host = host
		r.Header.Set("Origin", "https://panel.example.com")
		r.Header.Set("Content-Type", "application/json")
		err := a.maintenanceAuth(httptest.NewRecorder(), r, &maintenanceInput{})
		// An invalid JSON body must reach decoding, rather than fail Host auth.
		want := "bad_request"
		if host == "evil.example" || host == "panel.example.com:8443" {
			want = "forbidden"
		}
		var apiErr *httpx.Error
		if !errors.As(err, &apiErr) || apiErr.Code != want {
			t.Fatalf("unexpected result: %v", err)
		}
	}
}
