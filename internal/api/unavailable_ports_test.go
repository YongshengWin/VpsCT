package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnavailablePortBearerPermissionsIsolationAndReplay(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	read, _ := issueTestAPIKey(t, c, 1, "read")
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "port-record-member", "password": "test-fixture-password", "role": "user"}, 201)
	memberKey, _ := issueTestMemberSession(t, c, int64(member["id"].(float64)), "read", "write")
	server := c.do("POST", "/api/v1/servers", map[string]any{"name": "manual-port-records", "public_host": "example.test"}, 201)
	sid := int64(server["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d/unavailable-ports", sid)
	path := base + "/23456"
	body := map[string]any{"transport": "tcp", "network": "local network", "reason": "manual observation"}
	keyRequest(t, c, "PUT", path, read, body, nil, 403)
	keyRequest(t, c, "PUT", path, memberKey, body, nil, 403)
	keyRequest(t, c, "GET", base, memberKey, nil, nil, 403)
	keyRequest(t, c, "PUT", base+"/0", key, body, nil, 400)
	keyRequest(t, c, "PUT", base+"/65536", key, body, nil, 400)
	bad := map[string]any{"transport": "icmp", "network": "local"}
	keyRequest(t, c, "PUT", path, key, bad, nil, 400)
	bad = map[string]any{"transport": "both", "reason": strings.Repeat("x", 501)}
	keyRequest(t, c, "PUT", path, key, bad, nil, 400)
	keyRequest(t, c, "PUT", "/api/v1/servers/999999/unavailable-ports/23456", key, body, nil, 404)
	first, _ := keyRequest(t, c, "PUT", path, key, body, nil, 200)
	body["reason"] = "updated observation"
	second, _ := keyRequest(t, c, "PUT", path, key, body, nil, 200)
	if first["created_at"] != second["created_at"] || second["reason"] != "updated observation" {
		t.Fatal(first, second)
	}
	got, _ := keyRequest(t, c, "GET", path, read, nil, nil, 200)
	if got["server_id"] != float64(sid) || got["port"] != float64(23456) {
		t.Fatal(got)
	}
	_, raw := keyRequest(t, c, "GET", base, read, nil, nil, 200)
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 {
		t.Fatal(string(raw), err)
	}
	other := c.do("POST", "/api/v1/servers", map[string]any{"name": "other-port-records", "public_host": "other.example.test"}, 201)
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/servers/%.0f/unavailable-ports/23456", other["id"]), read, nil, nil, 404)
	keyRequest(t, c, "DELETE", path, read, nil, nil, 403)
	keyRequest(t, c, "DELETE", path, memberKey, nil, nil, 403)
	keyRequest(t, c, "DELETE", path, key, nil, nil, 204)
	keyRequest(t, c, "DELETE", path, key, nil, nil, 204)
	keyRequest(t, c, "GET", path, read, nil, nil, 404)
	var audited int
	if err := c.api.Store.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE action IN ('server.port.unavailable.save','server.port.unavailable.remove')`).Scan(&audited); err != nil || audited != 3 {
		t.Fatal("missing or repeated mutation audit", audited, err)
	}
}
func TestRetiredProbeRoutesAndCapabilitiesAreAbsent(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	for _, path := range []string{"/api/v1/nodes/1/test", "/api/v1/nodes/1/latency-tests", "/api/v1/access-routes/00000000000000000000000000000000/latency-tests"} {
		keyRequest(t, c, "POST", path, key, map[string]any{}, nil, 404)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/v1/latency-report", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	c.api.Handler().ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("agent probe route remains", w.Code)
	}
	_, raw := keyRequest(t, c, "GET", "/api/v1/agent/openapi.json", key, nil, nil, 200)
	for _, removed := range []string{"latency-tests", "/nodes/{id}/test", "link_latency", "node_test"} {
		if strings.Contains(string(raw), removed) {
			t.Fatal("obsolete probe capability", removed)
		}
	}
}
