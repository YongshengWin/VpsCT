package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/maintenance"
)

func issueTestAPIKey(t *testing.T, c *client, userID int64, scopes ...string) (string, domain.APIKey) {
	t.Helper()
	u, err := c.api.Store.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	plain := apiKeyPrefix + auth.RandomToken(32)
	k := domain.APIKey{UserID: userID, SecurityVersion: u.SecurityVersion, Name: "fixture", TokenHash: auth.HashToken(plain), TokenHint: plain[:12], Scopes: scopes}
	if err := c.api.Store.CreateAPIKey(context.Background(), &k); err != nil {
		t.Fatal(err)
	}
	return plain, k
}

// Uses no browser headers or cookies unless the test explicitly supplies them.
func keyRequest(t *testing.T, c *client, method, path, key string, body any, headers map[string]string, want int) (map[string]any, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, c.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	if len(resp.Cookies()) != 0 {
		t.Fatal("developer request unexpectedly created a browser session")
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, raw
}

func setupAPIKeyTest(t *testing.T) *client {
	t.Helper()
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	return c
}

func TestAPIKeyLifecycleAndBusinessAPI(t *testing.T) {
	c := setupAPIKeyTest(t)
	body := map[string]any{"name": "local-agent", "scopes": []string{"read", "write"}, "password": "wrong"}
	c.do("POST", "/api/v1/api-keys", body, 403)
	body["password"] = "password123"
	body["expires_at"] = time.Now().Add(time.Hour)
	out := c.do("POST", "/api/v1/api-keys", body, 201)
	key := out["key"].(string)
	meta := out["api_key"].(map[string]any)
	if !strings.HasPrefix(key, apiKeyPrefix) || meta["token_hash"] != nil || meta["security_version"] != nil {
		t.Fatal("credential format or metadata disclosure")
	}
	listed := c.do("GET", "/api/v1/api-keys", nil, 200)
	encoded, _ := json.Marshal(listed)
	if bytes.Contains(encoded, []byte(key)) || bytes.Contains(encoded, []byte(auth.HashToken(key))) {
		t.Fatal("list revealed a credential")
	}
	srv, _ := keyRequest(t, c, "POST", "/api/v1/servers", key, map[string]any{"name": "agent-created"}, nil, 201)
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/servers/%.0f", srv["id"]), key, nil, nil, 200)
	keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/servers/%.0f", srv["id"]), key, map[string]any{"name": "agent-updated"}, nil, 200)
	keyRequest(t, c, "DELETE", fmt.Sprintf("/api/v1/servers/%.0f", srv["id"]), key, nil, nil, 204)
	listed = c.do("GET", "/api/v1/api-keys", nil, 200)
	if listed["list"].([]any)[0].(map[string]any)["last_used_at"] == nil {
		t.Fatal("key usage was not recorded")
	}
	audits := c.do("GET", "/api/v1/audit", nil, 200)
	encoded, _ = json.Marshal(audits)
	if !bytes.Contains(encoded, []byte("api_key.request")) || !bytes.Contains(encoded, []byte("api_key_id")) || bytes.Contains(encoded, []byte(key)) {
		t.Fatal("missing key attribution or leaked credential in audit")
	}
	c.do("DELETE", fmt.Sprintf("/api/v1/api-keys/%.0f", meta["id"]), nil, 204)
	c.do("DELETE", fmt.Sprintf("/api/v1/api-keys/%.0f", meta["id"]), nil, 204)
	keyRequest(t, c, "GET", "/api/v1/dashboard", key, nil, nil, 401)
}

func TestAPIKeyScopesCoverEveryAuthenticatedRoute(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read")
	keyRequest(t, c, "GET", "/api/v1/dashboard", key, nil, nil, 200)
	for _, op := range c.api.agentOperations {
		if op.RequiredScope != "write" {
			continue
		}
		path := strings.NewReplacer("{id}", "1", "{iid}", "1", "{op}", "fixture", "{revision}", "1").Replace(op.Path)
		response, _ := keyRequest(t, c, op.Method, path, key, map[string]any{}, nil, 403)
		code := response["error"].(map[string]any)["code"]
		want := "insufficient_scope"
		if op.SessionOnly {
			want = "session_required"
		}
		if code != want {
			t.Fatalf("%s %s: %v want %s", op.Method, path, code, want)
		}
	}
}

func TestAPIKeyBrowserBoundaryAndCredentialIsolation(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	profile := map[string]any{"nickname": "agent"}
	for _, headers := range []map[string]string{
		{"Origin": "https://foreign.example.test"},
		{"Sec-Fetch-Site": "cross-site"},
		{"Origin": "null"},
	} {
		keyRequest(t, c, "PUT", "/api/v1/auth/profile", key, profile, headers, 403)
	}
	keyRequest(t, c, "PUT", "/api/v1/auth/profile", key, profile, map[string]string{"Content-Type": "text/plain"}, 415)
	keyRequest(t, c, "PUT", "/api/v1/auth/profile", key, profile, nil, 200)
	// Invalid/missing credentials cannot borrow an accompanying valid cookie.
	cookie := c.cookie.String()
	for _, header := range []string{"Bearer invalid", "Basic fixture", "Bearer ", ""} {
		keyRequest(t, c, "GET", "/api/v1/dashboard", "", nil, map[string]string{"Cookie": cookie, "Authorization": header}, 401)
	}
	keyRequest(t, c, "PUT", "/api/v1/auth/profile", "", profile, map[string]string{"Cookie": cookie, "Origin": c.srv.URL}, 403)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/api-keys"}, {"POST", "/api/v1/api-keys"}, {"DELETE", "/api/v1/api-keys/1"},
		{"GET", "/api/v1/auth/csrf"}, {"POST", "/api/v1/auth/2fa/setup"}, {"POST", "/api/v1/users/1/2fa/reset"},
		{"POST", "/api/v1/auth/login"}, {"POST", "/api/v1/auth/logout"}, {"POST", "/api/v1/auth/setup"},
	} {
		keyRequest(t, c, tc.method, tc.path, key, map[string]any{}, nil, 403)
	}
	keyRequest(t, c, "GET", "/api/agent/v1/desired", key, nil, nil, 401)
	keyRequest(t, c, "GET", "/api/v1/dashboard?api_key="+key, "", nil, nil, 401)
}

func TestAPIKeyMemberPermissionsAndExpiry(t *testing.T) {
	c := setupAPIKeyTest(t)
	created := c.do("POST", "/api/v1/users", map[string]any{"username": "member", "password": "password123", "role": "user"}, 201)
	key, _ := issueTestAPIKey(t, c, int64(created["id"].(float64)), "read", "write")
	keyRequest(t, c, "GET", "/api/v1/dashboard", key, nil, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/servers", key, nil, nil, 403)
	keyRequest(t, c, "POST", "/api/v1/nodes", key, map[string]any{}, nil, 403)
	_, raw := keyRequest(t, c, "GET", "/api/v1/subscriptions", key, nil, nil, 200)
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("member resources leaked: %s", raw)
	}
	adminKey, k := issueTestAPIKey(t, c, 1, "read")
	if err := c.api.Store.RevokeAPIKey(context.Background(), 1, k.ID); err != nil {
		t.Fatal(err)
	}
	keyRequest(t, c, "GET", "/api/v1/dashboard", adminKey, nil, nil, 401)
	plain := apiKeyPrefix + auth.RandomToken(32)
	u, _ := c.api.Store.GetUser(context.Background(), 1)
	past := c.api.Store.Now().Add(-time.Second)
	expired := domain.APIKey{UserID: 1, SecurityVersion: u.SecurityVersion, Name: "expired", TokenHash: auth.HashToken(plain), Scopes: []string{"read"}, ExpiresAt: &past}
	if err := c.api.Store.CreateAPIKey(context.Background(), &expired); err != nil {
		t.Fatal(err)
	}
	keyRequest(t, c, "GET", "/api/v1/dashboard", plain, nil, nil, 401)
}

func TestAPIKeyMaintenanceKeepsSecondAuthentication(t *testing.T) {
	c := setupAPIKeyTest(t)
	fake := &fakeMaintenance{}
	c.api.Deps.Maintenance = fake
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	in := maintenanceInput{Request: maintenance.Request{ID: maintenance.NewID(), Role: "controller", Action: "uninstall"}, Confirm: "VpsCT"}
	out, _ := keyRequest(t, c, "POST", "/api/v1/system/maintenance", key, in, nil, 403)
	if out["error"].(map[string]any)["code"] != "invalid_password" {
		t.Fatal("maintenance unexpectedly bypassed reauthentication or required browser Origin")
	}
	in.Password = "password123"
	in.Confirm = "wrong"
	keyRequest(t, c, "POST", "/api/v1/system/maintenance", key, in, nil, 400)
	in.Confirm = "VpsCT"
	keyRequest(t, c, "POST", "/api/v1/system/maintenance", key, in, nil, 202)
	if fake.calls != 1 {
		t.Fatal("unauthorized maintenance reached the executor")
	}
}

func TestAPIKeyCreationConsumesSecondFactor(t *testing.T) {
	c := setupAPIKeyTest(t)
	setup := c.do("POST", "/api/v1/auth/2fa/setup", map[string]string{"password": "password123"}, 200)
	secret := setup["secret"].(string)
	step := auth.TOTPStep(c.api.Store.Now())
	code, _ := auth.TOTPCode(secret, step)
	c.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": code}, 200)
	in := map[string]any{"name": "factor", "scopes": []string{"read"}, "password": "password123"}
	c.do("POST", "/api/v1/api-keys", in, 403)
	code, _ = auth.TOTPCode(secret, step+1)
	in["code"] = code
	c.do("POST", "/api/v1/api-keys", in, 201)
	c.do("POST", "/api/v1/api-keys", in, 403)
}

func TestAPIKeySSERevocation(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, k := issueTestAPIKey(t, c, 1, "read")
	r, _ := http.NewRequest("GET", c.srv.URL+"/api/v1/events", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status %d", resp.StatusCode)
	}
	if err := c.api.Store.RevokeAPIKey(context.Background(), 1, k.ID); err != nil {
		t.Fatal(err)
	}
	c.api.Events.Publish("fixture", map[string]any{"private": "must-not-arrive"})
	done := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(resp.Body); done <- b }()
	select {
	case b := <-done:
		if bytes.Contains(b, []byte("must-not-arrive")) {
			t.Fatal("revoked API key received an event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked key stream did not close")
	}
}

func TestAPIKeyDatabaseFailureAndRateLimit(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read")
	c.api.apiKeyLimiter = newRateLimiter(1, time.Minute)
	keyRequest(t, c, "GET", "/api/v1/dashboard", key, nil, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/dashboard", key, nil, nil, 429)
	if err := c.api.Store.Close(); err != nil {
		t.Fatal(err)
	}
	out, _ := keyRequest(t, c, "GET", "/api/v1/dashboard", key, nil, nil, 503)
	if out["error"].(map[string]any)["code"] != "auth_unavailable" {
		t.Fatal("database failure misclassified as revoked key")
	}
}
