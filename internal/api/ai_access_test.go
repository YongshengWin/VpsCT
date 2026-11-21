package api

import (
	"bytes"
	"context"

	"ctlvps/internal/domain"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestAIAccessFollowsPermissionAndKeyScope(t *testing.T) {
	c := setupAPIKeyTest(t)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "ai-member", "password": "password123"}, 201)
	id := int64(member["id"].(float64))
	adminKey, _ := issueTestAPIKey(t, c, 1, "read", "write")
	legacyKey, _ := issueTestAPIKey(t, c, id, "read", "write")
	session, _ := issueTestMemberSession(t, c, id)
	for _, path := range []string{"/api/v1/api-keys", "/api/v1/agent/capabilities", "/api/v1/agent/openapi.json", "/api/v1/agent/skill", "/api/v1/agent/skill.zip", "/api/v1/agent/client.py", "/api/v1/agent/reference"} {
		keyRequest(t, c, "GET", path, session, nil, nil, 403)
	}
	create := map[string]any{"name": "agent", "scopes": []string{"read"}, "password": "password123"}
	keyRequest(t, c, "POST", "/api/v1/api-keys", session, create, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/shares", legacyKey, nil, nil, 401)
	keyRequest(t, c, "GET", "/api/v1/shares", session, nil, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/me/templates", session, nil, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/agent/capabilities", adminKey, nil, nil, 200)

	grantPath := fmt.Sprintf("/api/v1/users/%d/permissions/", id)
	grantBody := map[string]any{"scope": map[string]any{"kind": "all"}, "password": "password123"}
	grant, _ := keyRequest(t, c, "PUT", grantPath+"ai.access", adminKey, grantBody, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/shares", session, nil, nil, 401)
	session, _ = issueTestMemberSession(t, c, id)
	keyRequest(t, c, "GET", "/api/v1/agent/capabilities", session, nil, nil, 200)
	result, _ := keyRequest(t, c, "POST", "/api/v1/api-keys", session, create, nil, 201)
	read := result["key"].(string)
	create["scopes"] = []string{"read", "write"}
	result, _ = keyRequest(t, c, "POST", "/api/v1/api-keys", session, create, nil, 201)
	rw := result["key"].(string)
	caps, _ := keyRequest(t, c, "GET", "/api/v1/agent/capabilities", read, nil, nil, 200)
	assertAIOperation(t, caps, "GET", "/api/v1/agent/capabilities", true, "ai.access")
	assertAIOperation(t, caps, "GET", "/api/v1/servers", false, "")
	assertAIOperation(t, caps, "POST", "/api/v1/me/templates", false, "")
	keyRequest(t, c, "GET", "/api/v1/shares", read, nil, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/servers", rw, nil, nil, 403)
	input := map[string]any{"name": "personal", "kind": "mihomo", "content": "proxy-groups: []\nrules: []\n"}
	keyRequest(t, c, "POST", "/api/v1/me/templates", read, input, nil, 403)
	keyRequest(t, c, "POST", "/api/v1/me/templates", rw, input, nil, 201)
	keyRequest(t, c, "POST", "/api/v1/templates", rw, input, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/api-keys", rw, nil, nil, 403)

	keyRequest(t, c, "PUT", grantPath+"templates.create", adminKey, grantBody, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/shares", rw, nil, nil, 401)
	session, _ = issueTestMemberSession(t, c, id)
	result, _ = keyRequest(t, c, "POST", "/api/v1/api-keys", session, create, nil, 201)
	rw = result["key"].(string)
	caps, _ = keyRequest(t, c, "GET", "/api/v1/agent/capabilities", rw, nil, nil, 200)
	assertAIOperation(t, caps, "POST", "/api/v1/templates", true, "templates.create")
	assertAIOperation(t, caps, "GET", "/api/v1/templates/{id}/versions", true, "")
	assertAIOperation(t, caps, "GET", "/api/v1/servers", false, "")
	keyRequest(t, c, "POST", "/api/v1/templates", rw, input, nil, 201)
	keyRequest(t, c, "GET", "/api/v1/servers", rw, nil, nil, 403)
	keyRequest(t, c, "DELETE", fmt.Sprintf("/api/v1/permissions/%d", int64(grant["id"].(float64))), adminKey, map[string]any{"password": "password123"}, nil, 204)
	keyRequest(t, c, "GET", "/api/v1/shares", rw, nil, nil, 401)
	session, _ = issueTestMemberSession(t, c, id)
	keyRequest(t, c, "GET", "/api/v1/agent/capabilities", session, nil, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/me/templates", session, nil, nil, 200)
	keyRequest(t, c, "PUT", grantPath+"ai.access", adminKey, grantBody, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/shares", rw, nil, nil, 401)
}

func assertAIOperation(t *testing.T, caps map[string]any, method, path string, available bool, permission string) {
	t.Helper()
	for _, raw := range caps["operations"].([]any) {
		op := raw.(map[string]any)
		if op["method"] == method && op["path"] == path {
			if op["available"] != available || (permission != "" && op["required_permission"] != permission) {
				t.Fatalf("incorrect capability for %s %s: %v", method, path, op)
			}
			return
		}
	}
	t.Fatalf("missing capability for %s %s", method, path)
}

func TestAIDelegationChainRevocation(t *testing.T) {
	c := setupAPIKeyTest(t)
	manager := c.do("POST", "/api/v1/users", map[string]any{"username": "ai-delegator", "password": "password123"}, 201)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "ai-recipient", "password": "password123"}, 201)
	managerID, memberID := int64(manager["id"].(float64)), int64(member["id"].(float64))
	admin, _ := issueTestAPIKey(t, c, 1, "read", "write")
	body := map[string]any{"scope": map[string]any{"kind": "all"}, "password": "password123"}
	var aiGrant map[string]any
	for _, perm := range []string{"accounts.edit", "accounts.delegate", "ai.access"} {
		g, _ := keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/users/%d/permissions/%s", managerID, perm), admin, body, nil, 200)
		if perm == "ai.access" {
			aiGrant = g
		}
	}
	managerSession, _ := issueTestMemberSession(t, c, managerID)
	keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/users/%d/permissions/ai.access", memberID), managerSession, body, nil, 200)
	memberSession, _ := issueTestMemberSession(t, c, memberID)
	keyRequest(t, c, "GET", "/api/v1/agent/capabilities", memberSession, nil, nil, 200)
	memberKey, _ := issueTestAPIKey(t, c, memberID, "read")
	keyRequest(t, c, "GET", "/api/v1/shares", memberKey, nil, nil, 200)
	keyRequest(t, c, "DELETE", fmt.Sprintf("/api/v1/permissions/%d", int64(aiGrant["id"].(float64))), admin, map[string]any{"password": "password123"}, nil, 204)
	keyRequest(t, c, "GET", "/api/v1/shares", memberKey, nil, nil, 401)
	memberSession, _ = issueTestMemberSession(t, c, memberID)
	keyRequest(t, c, "GET", "/api/v1/agent/capabilities", memberSession, nil, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/shares", memberSession, nil, nil, 200)
}

func TestAPIKeyOperationRestrictionsAndLiveUpdates(t *testing.T) {
	c := setupAPIKeyTest(t)
	a := c.do("POST", "/api/v1/users", map[string]any{"username": "restricted-a", "password": "password123"}, 201)
	b := c.do("POST", "/api/v1/users", map[string]any{"username": "restricted-b", "password": "password123"}, 201)
	aID, bID := int64(a["id"].(float64)), int64(b["id"].(float64))
	admin, _ := issueTestAPIKey(t, c, 1, "read", "write")
	body := map[string]any{"scope": map[string]any{"kind": "all"}, "password": "password123"}
	for _, id := range []int64{aID, bID} {
		keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/users/%d/permissions/ai.access", id), admin, body, nil, 200)
	}
	session, _ := issueTestMemberSession(t, c, aID)
	other, _ := issueTestMemberSession(t, c, bID)
	shares := agentOperationID("GET /api/v1/shares")
	templates := agentOperationID("GET /api/v1/me/templates")
	createTemplate := agentOperationID("POST /api/v1/me/templates")
	create := map[string]any{"name": "restricted", "scopes": []string{"read"}, "operations": []string{shares, createTemplate}, "password": "password123"}
	result, _ := keyRequest(t, c, "POST", "/api/v1/api-keys", session, create, nil, 201)
	key := result["key"].(string)
	id := int64(result["api_key"].(map[string]any)["id"].(float64))
	path := fmt.Sprintf("/api/v1/api-keys/%d/operations", id)
	keyRequest(t, c, "GET", "/api/v1/shares", key, nil, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/me/templates", key, nil, nil, 403)
	keyRequest(t, c, "POST", "/api/v1/me/templates", key, map[string]any{}, nil, 403)
	caps, _ := keyRequest(t, c, "GET", "/api/v1/agent/capabilities", key, nil, nil, 200)
	assertAIOperation(t, caps, "GET", "/api/v1/shares", true, "")
	assertAIOperation(t, caps, "GET", "/api/v1/me/templates", false, "")
	assertAIOperation(t, caps, "POST", "/api/v1/me/templates", false, "")
	update := map[string]any{"operations": []string{templates}, "password": "password123"}
	keyRequest(t, c, "PUT", path, key, update, nil, 403)
	keyRequest(t, c, "PUT", path, other, update, nil, 404)
	keyRequest(t, c, "PUT", path, session, map[string]any{"operations": []string{templates}, "password": "wrong"}, nil, 403)
	keyRequest(t, c, "PUT", path, session, map[string]any{"operations": []string{"unknown"}, "password": "password123"}, nil, 400)
	keyRequest(t, c, "PUT", path, session, update, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/shares", key, nil, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/me/templates", key, nil, nil, 200)
	caps, _ = keyRequest(t, c, "GET", "/api/v1/agent/capabilities", key, nil, nil, 200)
	assertAIOperation(t, caps, "GET", "/api/v1/shares", false, "")
	assertAIOperation(t, caps, "GET", "/api/v1/me/templates", true, "")
	keyRequest(t, c, "PUT", path, session, map[string]any{"operations": []string{}, "password": "password123"}, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/me/templates", key, nil, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/agent/capabilities", key, nil, nil, 200)
	keyRequest(t, c, "PUT", path, session, map[string]any{"operations": nil, "password": "password123"}, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/shares", key, nil, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/servers", key, nil, nil, 403)
}

func TestAPIKeyOperationChangeClosesEventStream(t *testing.T) {
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
	c.do("PUT", fmt.Sprintf("/api/v1/api-keys/%d/operations", k.ID), map[string]any{"operations": []string{}, "password": "password123"}, 200)
	c.api.Events.Publish("fixture", map[string]any{"private": "must-not-arrive"})
	done := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(resp.Body); done <- b }()
	select {
	case b := <-done:
		if bytes.Contains(b, []byte("must-not-arrive")) {
			t.Fatal("restricted key received event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restricted key stream did not close")
	}
}

func TestMemberAIStaysWithinShareAndResourceBoundary(t *testing.T) {
	c := setupAPIKeyTest(t)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "share-ai", "password": "password123"}, 201)
	other := c.do("POST", "/api/v1/users", map[string]any{"username": "other-owner", "password": "password123"}, 201)
	id, otherID := int64(member["id"].(float64)), int64(other["id"].(float64))
	ctx := context.Background()
	own := domain.Share{Name: "own", UserID: &id}
	foreign := domain.Share{Name: "foreign", UserID: &otherID}
	for _, sh := range []*domain.Share{&own, &foreign} {
		if err := c.api.Store.CreateShare(ctx, sh); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := issueTestAPIKey(t, c, 1, "read", "write")
	keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/users/%d/permissions/ai.access", id), admin, map[string]any{"scope": map[string]any{"kind": "all"}, "password": "password123"}, nil, 200)
	memberKey, _ := issueTestAPIKey(t, c, id, "read", "write")
	otherSession, _ := issueTestMemberSession(t, c, otherID)
	personal, _ := keyRequest(t, c, "POST", "/api/v1/me/templates", otherSession, map[string]any{"name": "private", "kind": "mihomo", "content": "proxy-groups: []\nrules: []\n"}, nil, 201)
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/shares/%d", own.ID), memberKey, nil, nil, 200)
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/shares/%d", foreign.ID), memberKey, nil, nil, 403)
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/me/templates/%d", int64(personal["id"].(float64))), memberKey, nil, nil, 404)
	keyRequest(t, c, "POST", fmt.Sprintf("/api/v1/shares/%d/revoke", own.ID), memberKey, nil, nil, 403)
	for _, path := range []string{"/api/v1/servers", "/api/v1/nodes", "/api/v1/subscriptions", "/api/v1/users", "/api/v1/dashboard", "/api/v1/presets", "/api/v1/templates/1/versions"} {
		keyRequest(t, c, "GET", path, memberKey, nil, nil, 403)
	}
	caps, _ := keyRequest(t, c, "GET", "/api/v1/agent/capabilities", memberKey, nil, nil, 200)
	for _, path := range []string{"/api/v1/servers", "/api/v1/nodes", "/api/v1/subscriptions", "/api/v1/users", "/api/v1/dashboard", "/api/v1/presets", "/api/v1/templates/{id}/versions"} {
		assertAIOperation(t, caps, "GET", path, false, "")
	}
	assertAIOperation(t, caps, "GET", "/api/v1/shares", true, "")
	session, _ := issueTestMemberSession(t, c, id)
	keyRequest(t, c, "POST", "/api/v1/api-keys", session, map[string]any{"name": "invalid", "scopes": []string{"read"}, "operations": []string{agentOperationID("GET /api/v1/subscriptions")}, "password": "password123"}, nil, 400)
}

func TestMemberAIRespectsScopedShareGrants(t *testing.T) {
	c := setupAPIKeyTest(t)
	manager := c.do("POST", "/api/v1/users", map[string]any{"username": "share-ai-manager", "password": "password123"}, 201)
	owner := c.do("POST", "/api/v1/users", map[string]any{"username": "share-ai-owner", "password": "password123"}, 201)
	managerID, ownerID := int64(manager["id"].(float64)), int64(owner["id"].(float64))
	sh := domain.Share{Name: "assigned", UserID: &ownerID, Notes: "private"}
	other := domain.Share{Name: "outside", UserID: &ownerID}
	for _, item := range []*domain.Share{&sh, &other} {
		if err := c.api.Store.CreateShare(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := issueTestAPIKey(t, c, 1, "read", "write")
	path := fmt.Sprintf("/api/v1/users/%d/permissions/", managerID)
	keyRequest(t, c, "PUT", path+"ai.access", admin, map[string]any{"scope": map[string]any{"kind": "all"}, "password": "password123"}, nil, 200)
	keyRequest(t, c, "PUT", path+"shares.view", admin, map[string]any{"scope": map[string]any{"kind": "shares", "ids": []int64{sh.ID}}, "password": "password123"}, nil, 200)
	key, _ := issueTestAPIKey(t, c, managerID, "read", "write")
	result, _ := keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/shares/%d", sh.ID), key, nil, nil, 200)
	if result["subscription"] != nil || result["nodes"] != nil || result["notes"] != "" {
		t.Fatal("share view exposed fields requiring link access")
	}
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/shares/%d", other.ID), key, nil, nil, 403)
	keyRequest(t, c, "POST", fmt.Sprintf("/api/v1/shares/%d/revoke", sh.ID), key, nil, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/servers", key, nil, nil, 403)
}
