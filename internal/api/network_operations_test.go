package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
)

func TestNetworkOperationsAPITracksExactReceiptsAndAuthorizedRetries(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "operations fixture"}, 201)
	sid := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	e := c.do("POST", base+"/enroll-token", nil, 200)
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: e["token"].(string)}, 200)
	c.agent = en["agent_token"].(string)
	server, err := c.api.Store.GetServer(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	node, err := provision.NewNode(server, "", provision.Options{Protocol: "ss", Port: 21001})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.api.Store.CreateNode(ctx, &node); err != nil {
		t.Fatal(err)
	}
	op, err := c.api.Store.RequestNodeNetwork(ctx, store.NodeNetworkRequest{ID: strings.Repeat("a", 32), NodeID: node.ID, Network: &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}}, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/network/operations/" + op.ID
	get := func(want string) {
		t.Helper()
		v := c.do("GET", path, nil, 200)
		if v["status"] != want {
			t.Fatal(v)
		}
		wire, _ := json.Marshal(v)
		for _, private := range []string{"request_hash", "desired_hash", "password", "fixture-private-apply-error"} {
			if bytes.Contains(wire, []byte(private)) {
				t.Fatal("operation view leaked internal details")
			}
		}
	}
	get("queued")
	list := c.do("GET", base+"/network/operations?limit=1", nil, 200)["list"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != op.ID {
		t.Fatal("operation listing lost saved intent")
	}
	if len(c.do("GET", base+"/network/operations?offset=1", nil, 200)["list"].([]any)) != 0 {
		t.Fatal("operation listing ignored pagination")
	}
	c.do("GET", "/api/v1/network/operations/invalid", nil, 400)
	c.do("GET", "/api/v1/network/operations/"+strings.Repeat("f", 32), nil, 404)
	c.do("POST", path+"/retry", map[string]any{}, 400)
	c.do("POST", path+"/retry", map[string]any{"expected_retry_revision": 0}, 409)
	if err = c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	op, _ = c.api.Store.NetworkOperation(ctx, op.ID)
	get("waiting_agent")
	// Both receipt channels require the exact revision and hash.
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: op.DesiredRevision, Hash: strings.Repeat("f", 64), Status: "applied"}, 204)
	get("waiting_agent")
	c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{AppliedRevision: op.DesiredRevision + 1, AppliedHash: op.DesiredHash, ApplyStatus: "applied"}, 200)
	get("waiting_agent")
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: op.DesiredRevision, Hash: op.DesiredHash, Status: "failed", Error: "fixture-private-apply-error"}, 204)
	get("apply_failed")
	// Authenticated writes still require CSRF, including async retries.
	req, _ := http.NewRequest("POST", c.srv.URL+path+"/retry", strings.NewReader(`{"expected_retry_revision":0}`))
	req.AddCookie(c.cookie)
	req.Header.Set("Origin", c.srv.URL)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("retry did not require CSRF", res.StatusCode)
	}
	retry := c.do("POST", path+"/retry", map[string]any{"expected_retry_revision": 0}, 202)
	if retry["status"] != "queued" || retry["retry_revision"] != float64(1) {
		t.Fatal(retry)
	}
	c.do("POST", path+"/retry", map[string]any{"expected_retry_revision": 0}, 409)
	if err = c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: op.DesiredRevision, Hash: op.DesiredHash, Status: "applied"}, 204)
	get("waiting_agent")
	op, _ = c.api.Store.NetworkOperation(ctx, op.ID)
	c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{AppliedRevision: op.DesiredRevision, AppliedHash: op.DesiredHash, ApplyStatus: "applied"}, 200)
	get("applied")
	c.do("POST", path+"/retry", map[string]any{"expected_retry_revision": 1}, 409)
	audits, err := c.api.Store.ListAudit(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range audits {
		if event.Action == "network.operation.retry" {
			count++
			if event.UserID == nil || event.Username != "admin" || event.Target != op.ID {
				t.Fatal("retry audit lost authenticated actor")
			}
		}
	}
	if count != 1 {
		t.Fatal("retry audit lost or duplicated", count)
	}
	c.do("POST", "/api/v1/users", map[string]any{"username": "viewer", "password": "password123", "role": "user", "enabled": true}, 201)
	c.cookie = nil
	c.do("GET", path, nil, 401)
	c.do("POST", path+"/retry", map[string]any{"expected_retry_revision": 1}, 401)
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "viewer", "password": "password123"}, 200)
	c.do("GET", path, nil, 403)
	c.do("GET", base+"/network/operations", nil, 403)
	c.do("POST", path+"/retry", map[string]any{"expected_retry_revision": 1}, 403)
}
