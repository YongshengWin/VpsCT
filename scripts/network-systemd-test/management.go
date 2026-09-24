//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
)

// Use real admin authentication, CSRF, preview and mutation routes in the
// disposable fixture, rather than bypassing management admission via Store.
type fixtureAdmin struct {
	ctx       context.Context
	client    *http.Client
	url, csrf string
}

func newFixtureAdmin(ctx context.Context, server *httptest.Server) *fixtureAdmin {
	client := server.Client()
	jar, err := cookiejar.New(nil)
	must(err)
	client.Jar = jar
	a := &fixtureAdmin{ctx: ctx, client: client, url: server.URL}
	a.request("POST", "/api/v1/auth/setup", map[string]any{"setup_token": "isolated-fixture-setup-only", "username": "fixture-admin", "password": "isolated-fixture-login-only"}, http.StatusOK, nil)
	var csrf struct {
		Token string `json:"token"`
	}
	a.request("GET", "/api/v1/auth/csrf", nil, http.StatusOK, &csrf)
	if csrf.Token == "" {
		panic("fixture missing CSRF token")
	}
	a.csrf = csrf.Token
	return a
}

func (a *fixtureAdmin) request(method, path string, body any, want int, out any) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		must(err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(a.ctx, method, a.url+path, rd)
	must(err)
	req.Header.Set("Origin", a.url)
	req.Header.Set("Content-Type", "application/json")
	if a.csrf != "" {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	res, err := a.client.Do(req)
	must(err)
	defer res.Body.Close()
	if res.StatusCode != want {
		panic(fmt.Sprintf("fixture management %s %s: status %d, want %d", method, path, res.StatusCode, want))
	}
	if out != nil {
		must(json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out))
	}
}
