package api

import (
	"context"
	"ctlvps/internal/domain"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentWorkflowsResolveLiveOperationsAndPermissions(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read")
	out, _ := keyRequest(t, c, "GET", "/api/v1/agent/capabilities", key, nil, nil, 200)
	workflows := out["workflows"].([]any)
	if len(workflows) < 10 {
		t.Fatal("missing product workflows")
	}
	seen := map[string]bool{}
	for _, raw := range workflows {
		flow := raw.(map[string]any)
		id := flow["id"].(string)
		if seen[id] {
			t.Fatal("duplicate workflow", id)
		}
		seen[id] = true
		for _, rawStep := range flow["steps"].([]any) {
			step := rawStep.(map[string]any)
			if step["operation_id"] == "" {
				t.Fatalf("unregistered workflow step: %s %s", step["method"], step["path"])
			}
			if step["available"] != (step["method"] == "GET") {
				t.Fatal("workflow bypasses read-only scope", id, step)
			}
		}
	}
	deployment := out["deployment"].(map[string]any)
	if deployment["requires_host_authorization"] != true || deployment["api_key_creation"] != "session_only" {
		t.Fatal("bootstrap authorization was broadened")
	}
}

func TestAccessRouteBearerLifecycleBothTopologies(t *testing.T) {
	for _, name := range []string{"two", "three", "imported_vless"} {
		t.Run(name, func(t *testing.T) {
			c, in, tokens := accessRouteAPIFixture(t)
			key, _ := issueTestAPIKey(t, c, 1, "read", "write")
			read, _ := issueTestAPIKey(t, c, 1, "read")
			if name == "two" {
				delete(tokens, in.IXServerID)
				in.IXServerID = 0
			}
			if name == "imported_vless" {
				landing := domain.Node{Name: "external VLESS", Protocol: "vless", Server: "landing.example.test", Port: 443, Source: domain.NodeImported, Enabled: true, Params: json.RawMessage(`{"uuid":"11111111-1111-1111-1111-111111111111","tls":true,"network":"ws"}`)}
				if err := c.api.Store.CreateNode(context.Background(), &landing); err != nil {
					t.Fatal(err)
				}
				in.LandingNodeID = landing.ID
				in.LandingHost = "203.0.113.30"
			}
			member := c.do("POST", "/api/v1/users", map[string]any{"username": "member", "password": "password123", "role": "user"}, 201)
			memberKey, _ := issueTestAPIKey(t, c, int64(member["id"].(float64)), "read", "write")
			keyRequest(t, c, "GET", "/api/v1/access-routes/"+in.ID, memberKey, nil, nil, 403)
			keyRequest(t, c, "POST", "/api/v1/access-routes/preview", memberKey, in, nil, 403)
			keyRequest(t, c, "POST", "/api/v1/access-routes/preview", read, in, nil, 403)
			preview, _ := keyRequest(t, c, "POST", "/api/v1/access-routes/preview", key, in, nil, 200)
			if preview["ready"] != true {
				t.Fatal("preview not ready")
			}
			raw, _ := json.Marshal(preview["candidate"])
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			in.ExpectedImpact = preview["token"].(string)
			created, _ := keyRequest(t, c, "POST", "/api/v1/access-routes", key, in, nil, 202)
			replay, _ := keyRequest(t, c, "POST", "/api/v1/access-routes", key, in, nil, 202)
			if created["id"] != in.ID || replay["id"] != in.ID {
				t.Fatal("idempotent creation lost identity")
			}
			keyRequest(t, c, "GET", "/api/v1/access-routes/"+in.ID, "", nil, nil, 401)
			keyRequest(t, c, "GET", "/api/v1/access-routes/"+strings.Repeat("e", 32), read, nil, nil, 404)
			keyRequest(t, c, "GET", "/api/v1/access-routes/"+in.ID+"/node", key, nil, nil, 409)
			for i := 0; i < 4; i++ {
				acknowledgeRouteAPI(t, c, tokens)
			}
			current, _ := keyRequest(t, c, "GET", "/api/v1/access-routes/"+in.ID, read, nil, nil, 200)
			if current["stage"] != "applied" {
				t.Fatal("not applied", current["stage"])
			}
			keyRequest(t, c, "GET", "/api/v1/access-routes/"+in.ID+"/node", key, nil, nil, 200)
			keyRequest(t, c, "DELETE", "/api/v1/access-routes/"+in.ID, key, map[string]any{"expected_revision": 0}, nil, 409)
			keyRequest(t, c, "DELETE", "/api/v1/access-routes/"+in.ID, key, map[string]any{"expected_revision": current["revision"]}, nil, 202)
			for i := 0; i < 5; i++ {
				acknowledgeRouteAPI(t, c, tokens)
			}
			stopped, _ := keyRequest(t, c, "GET", "/api/v1/access-routes/"+in.ID, read, nil, nil, 200)
			if stopped["stage"] != "stopping_entry" {
				t.Fatal("cleanup must wait for final accounting", stopped["stage"])
			}
			keyRequest(t, c, "GET", "/api/v1/access-routes/"+in.ID+"/node", key, nil, nil, 409)
		})
	}
}

func TestAgentRouteContractsDescribeActualWorkflow(t *testing.T) {
	c := setupAPIKeyTest(t)
	key, _ := issueTestAPIKey(t, c, 1, "read")
	doc, _ := keyRequest(t, c, "GET", "/api/v1/agent/openapi.json", key, nil, nil, 200)
	paths := doc["paths"].(map[string]any)
	for _, check := range []struct{ path, method, status string }{
		{"/api/v1/access-routes/{op}", "get", "200"},
		{"/api/v1/access-routes/preview", "post", "200"},
		{"/api/v1/access-routes", "post", "202"},
	} {
		op := paths[check.path].(map[string]any)[check.method].(map[string]any)
		response := op["responses"].(map[string]any)[check.status].(map[string]any)
		schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		p := schema["properties"].(map[string]any)
		if check.method == "get" && (p["stage"] == nil || p["revision"] == nil || p["message"] == nil) {
			t.Fatal("missing poll contract")
		}
		if check.path == "/api/v1/access-routes/preview" && (p["candidate"] == nil || p["token"] == nil || p["ready"] == nil) {
			t.Fatal("missing preview contract")
		}
	}
	for _, create := range []bool{false, true} {
		shape := agentAccessRouteRequestSchema(create)
		raw, _ := json.Marshal(shape)
		for _, field := range []string{"ix_server_id", "operation_id", "landing_node_id", "expected_impact", "source_mode"} {
			if !strings.Contains(string(raw), field) {
				t.Fatal("missing request field", field)
			}
		}
	}
	// Registration must not mutate the generated shared schema.
	if agentContracts["createAccessRoute"].RequestBody["required"] != nil {
		t.Fatal("mutated generated contract")
	}
}
