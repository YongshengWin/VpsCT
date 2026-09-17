package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
)

func TestSurgeManagedBody(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/s/test-capability/surge", "https://panel.example.test/s/test-capability/surge"},
		{"/s/test-capability?format=surge&ignored=value", "https://panel.example.test/s/test-capability?format=surge"},
		{"/r/test-short", "https://panel.example.test/r/test-short?format=surge"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			u, _ := url.Parse(tc.path)
			original := "\ufeff#!MANAGED-CONFIG https://other.example.test/old interval=60\n[Proxy]\nfront = direct\n"
			got := string(surgeManagedBody([]byte(original), "https://panel.example.test", u))
			want := "#!MANAGED-CONFIG " + tc.want + " interval=3600 strict=false\n[Proxy]\nfront = direct\n"
			if got != want {
				t.Fatalf("unexpected managed profile: %q", got)
			}
		})
	}
	u, _ := url.Parse("/s/test/surge")
	for _, base := range []string{"", "file:///tmp/profile", "https://user:pass@panel.example.test", "https://panel.example.test\n[Rule]"} {
		if got := string(surgeManagedBody([]byte("original"), base, u)); got != "original" {
			t.Fatal("invalid public URL must not become a directive")
		}
	}
}

func TestPublicSurgeManagedSubscription(t *testing.T) {
	c := newTestAPI(t)
	c.api.Config.SiteURL = "https://panel.example.test"
	token := auth.NewSubscriptionToken()
	sub := domain.Subscription{Name: "managed", Kind: "generated", Enabled: true, Token: token, TokenHash: auth.HashToken(token), ShortCode: "managed-short", NodeSelection: domain.NodeSelection{IncludeAll: true}}
	if err := c.api.Store.CreateSubscription(context.Background(), &sub); err != nil {
		t.Fatal(err)
	}
	get := func(path string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, c.srv.URL+path, nil)
		if err != nil {
			return nil, err
		}
		req.Host = "panel.example.test"
		return http.DefaultClient.Do(req)
	}
	for _, path := range []string{"/s/" + token + "/surge", "/s/" + token + "?format=surge", "/r/managed-short?format=surge"} {
		resp, err := get(path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("response: %d %v", resp.StatusCode, err)
		}
		if !strings.HasPrefix(string(body), "#!MANAGED-CONFIG https://panel.example.test"+path+" interval=3600 strict=false\n") {
			t.Fatal("missing self-referencing update directive")
		}
		if !strings.Contains(string(body), "[Proxy]") {
			t.Fatal("proxy section lost")
		}
	}
	resp, err := get("/s/" + token + "/mihomo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.Contains(string(body), "#!MANAGED-CONFIG") {
		t.Fatal("other formats must remain unchanged")
	}
}
