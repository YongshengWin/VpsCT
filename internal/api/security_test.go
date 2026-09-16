package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
)

func TestBrowserWriteBoundary(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	for _, tc := range []struct {
		name, origin, csrf, media string
		status                    int
	}{
		{"missing origin", "", "valid", "application/json", 403},
		{"foreign", "https://evil.test", "valid", "application/json", 403},
		{"null", "null", "valid", "application/json", 403},
		{"missing csrf", c.srv.URL, "", "application/json", 403},
		{"wrong csrf", c.srv.URL, "wrong", "application/json", 403},
		{"form", c.srv.URL, "valid", "text/plain", 415},
		{"valid", c.srv.URL, "valid", "application/json", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("PUT", c.srv.URL+"/api/v1/auth/profile", strings.NewReader(`{"nickname":"safe"}`))
			r.AddCookie(c.cookie)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.media)
			v := tc.csrf
			if v == "valid" {
				v = c.api.csrfToken(c.cookie.Value)
			}
			r.Header.Set("X-CSRF-Token", v)
			resp, e := http.DefaultClient.Do(r)
			if e != nil {
				t.Fatal(e)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d want %d", resp.StatusCode, tc.status)
			}
		})
	}
	c.do("PUT", "/api/v1/auth/profile", json.RawMessage(`{"nickname":"one","Nickname":"two"}`), 400)
}

func TestMemberCannotManageOrReadOtherResources(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	u, e := c.api.Store.GetUser(context.Background(), 1)
	if e != nil {
		t.Fatal(e)
	}
	u.Role = domain.RoleUser
	if e = c.api.Store.UpdateUser(context.Background(), &u); e != nil {
		t.Fatal(e)
	}
	c.do("GET", "/api/v1/servers", nil, 401)
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "password123"}, 200)
	for _, path := range []string{"/api/v1/servers", "/api/v1/nodes", "/api/v1/settings", "/api/v1/users", "/api/v1/audit", "/api/v1/system/maintenance"} {
		c.do("GET", path, nil, 403)
	}
	for _, path := range []string{"/api/v1/servers", "/api/v1/nodes/bulk-delete", "/api/v1/agents/update"} {
		c.do("POST", path, map[string]any{"ids": []int{1, 2}}, 403)
	}
}

func TestConcurrentEnrollmentSingleWinner(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	c.do("POST", "/api/v1/servers", map[string]any{"name": "fixture"}, 201)
	out := c.do("POST", "/api/v1/servers/1/enroll-token", nil, 200)
	token := out["token"].(string)
	ag, e := c.api.Store.GetAgentByEnrollHash(context.Background(), auth.HashToken(token))
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			results <- c.api.Store.CompleteEnrollment(context.Background(), ag.ID, ag.EnrollTokenHash, auth.HashToken(auth.RandomToken(32)), "test")
		})
	}
	wg.Wait()
	close(results)
	n := 0
	for e := range results {
		if e == nil {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("winners=%d", n)
	}
}

func TestAvatarRejectsSpoofedAndActiveContent(t *testing.T) {
	for _, v := range []string{"data:image/png;base64,PGh0bWw+", "data:image/svg+xml;base64,PHN2Zz4=", "https://example.test/avatar"} {
		if _, e := normalizeAvatar(v); e == nil {
			t.Fatal("accepted invalid avatar")
		}
	}
}

func TestCompressedBatchBudget(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	c.do("POST", "/api/v1/servers", map[string]any{"name": "fixture"}, 201)
	tok := c.do("POST", "/api/v1/servers/1/enroll-token", nil, 200)["token"].(string)
	c.agent = c.do("POST", "/api/agent/v1/enroll", map[string]any{"enroll_token": tok}, 200)["agent_token"].(string)
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	_, _ = io.Copy(z, strings.NewReader(strings.Repeat(" ", 9<<20)))
	_ = z.Close()
	r := httptest.NewRequest("POST", "http://example.test/api/agent/v1/connlog", &b)
	r.Header.Set("Authorization", "Bearer "+c.agent)
	r.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	c.api.Handler().ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestSSEBudgetAndRevocation(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	var streams []*http.Response
	defer func() {
		for _, s := range streams {
			s.Body.Close()
		}
	}()
	for i := 0; i < 6; i++ {
		r, _ := http.NewRequest("GET", c.srv.URL+"/api/v1/events", nil)
		r.AddCookie(c.cookie)
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		if i < 5 {
			if resp.StatusCode != 200 {
				t.Fatal(resp.StatusCode)
			}
			streams = append(streams, resp)
		} else {
			resp.Body.Close()
			if resp.StatusCode != 429 {
				t.Fatal("SSE cap not enforced")
			}
		}
	}
	c.do("GET", "/healthz", nil, 200)
	if e := c.api.Store.DeleteUserSessions(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	c.api.Events.Publish("fixture", map[string]any{"secret": "must-not-arrive"})
	done := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(streams[0].Body); done <- b }()
	select {
	case b := <-done:
		if bytes.Contains(b, []byte("must-not-arrive")) {
			t.Fatal("revoked session saw event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked stream did not close")
	}
}

func TestAuthFloodDoesNotStarveAgent(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	c.do("POST", "/api/v1/servers", map[string]any{"name": "load-fixture"}, 201)
	token := c.do("POST", "/api/v1/servers/1/enroll-token", nil, 200)["token"].(string)
	c.agent = c.do("POST", "/api/agent/v1/enroll", map[string]any{"enroll_token": token}, 200)["agent_token"].(string)
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	var rejected atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, _ := http.NewRequest("POST", c.srv.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"invalid-fixture-password"}`))
			r.Header.Set("Origin", c.srv.URL)
			r.Header.Set("Content-Type", "application/json")
			resp, e := http.DefaultClient.Do(r)
			if e == nil {
				if resp.StatusCode == 429 {
					rejected.Add(1)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	close(start)
	began := time.Now()
	c.do("POST", "/api/agent/v1/heartbeat", map[string]any{}, 200)
	latency := time.Since(began)
	wg.Wait()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if rejected.Load() == 0 {
		t.Fatal("flood not bounded")
	}
	if latency > 5*time.Second {
		t.Fatal("heartbeat starved")
	}
	t.Logf("requests=40 rejected=%d heartbeat=%s heap-before=%dMiB heap-after=%dMiB goroutines=%d", rejected.Load(), latency, baseline.HeapAlloc>>20, after.HeapAlloc>>20, runtime.NumGoroutine())
}
