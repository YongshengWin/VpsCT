package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"
	"time"
)

func TestEventStreamKeepsAuthenticationHealthy(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	endStream := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			go func() {
				select {
				case <-endStream:
					cancel()
				case <-ctx.Done():
				}
			}()
			r = r.WithContext(ctx)
		}
		c.api.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	transport := &http.Transport{MaxConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 32 * time.Second}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/events", nil)
	req.AddCookie(c.cookie)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				c.api.Events.Publish("test.keepalive", map[string]bool{"ok": true})
			}
		}
	}()
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(res.Body)
	ping := false
	for scanner.Scan() {
		if scanner.Text() == ": ping" {
			ping = true
			close(endStream)
			break
		}
	}
	_, err = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("GET", server.URL+"/api/v1/auth/me", nil)
	req.AddCookie(c.cookie)
	reused := false
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	next, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Body.Close()
	b, _ := io.ReadAll(next.Body)
	if !ping || !reused || next.StatusCode != 200 {
		t.Fatalf("ping=%v reused=%v subsequent authentication=%d %s", ping, reused, next.StatusCode, b)
	}
}
