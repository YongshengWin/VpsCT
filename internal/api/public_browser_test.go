package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
)

func TestSubscriptionBrowserAccess(t *testing.T) {
	c := newTestAPI(t)
	token := auth.NewSubscriptionToken()
	sub := domain.Subscription{Name: "browser-test", Kind: domain.SubGenerated, Enabled: true, Token: token, TokenHash: auth.HashToken(token), ShortCode: "browser-test-short", DefaultFormat: "mihomo", NodeSelection: domain.NodeSelection{IncludeAll: true}}
	if err := c.api.Store.CreateSubscription(context.Background(), &sub); err != nil {
		t.Fatal(err)
	}
	requestIndex := 0
	for _, path := range []string{"/s/" + token, "/s/" + token + "/mihomo", "/s/" + token + "?format=surge", "/r/browser-test-short", "/r/browser-test-short?format=surge"} {
		for _, tc := range []struct {
			name, ua, mode, dest string
			browser              bool
		}{
			{name: "chrome", ua: "Mozilla/5.0 Chrome/130.0 Safari/537.36", browser: true},
			{name: "safari", ua: "Mozilla/5.0 (iPhone) AppleWebKit/605.1.15 Version/18.0 Mobile Safari/604.1", browser: true},
			{name: "firefox", ua: "Mozilla/5.0 Firefox/130.0", browser: true},
			{name: "navigation", ua: "Surge iOS/3000", mode: "navigate", browser: true},
			{name: "document", dest: "document", browser: true},
			{name: "surge", ua: "Surge iOS/3000"},
			{name: "mihomo", ua: "clash.meta"},
			{name: "shadowrocket", ua: "Shadowrocket/2000"},
			{name: "singbox", ua: "sing-box/1.12"},
			{name: "v2ray", ua: "v2rayN/7.0"},
			{name: "mixed-client", ua: "Mozilla/5.0 Clash-Verge/2.0"},
			{name: "converter", ua: "Go-http-client/1.1"},
			{name: "empty"},
		} {
			t.Run(path[len(path)-min(len(path), 12):]+"/"+tc.name, func(t *testing.T) {
				req, err := http.NewRequest(http.MethodGet, c.srv.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("User-Agent", tc.ua)
				req.Header.Set("Sec-Fetch-Mode", tc.mode)
				req.Header.Set("Sec-Fetch-Dest", tc.dest)
				requestIndex++
				req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", requestIndex)
				recorder := httptest.NewRecorder()
				c.api.Handler().ServeHTTP(recorder, req)
				resp := recorder.Result()
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if tc.browser {
					if resp.StatusCode != http.StatusForbidden || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(body), "请在订阅客户端中使用") {
						t.Fatalf("expected browser prompt: %d %s", resp.StatusCode, body)
					}
					if resp.Header.Get("Content-Disposition") != "" || resp.Header.Get("Subscription-Userinfo") != "" || strings.Contains(string(body), token) || strings.Contains(string(body), "proxies:") {
						t.Fatal("browser response exposed profile or download headers")
					}
				} else if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") || strings.Contains(string(body), "<!doctype html>") {
					t.Fatalf("client download failed: %d %s", resp.StatusCode, body)
				}
				if resp.Header.Get("Cache-Control") != "no-store" {
					t.Fatal("response must not be cached")
				}
			})
		}
	}
}
