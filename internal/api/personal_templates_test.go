package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"ctlvps/internal/domain"
)

func TestPersonalTemplatesSessionIsolationAndShareTransfer(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	a := c.do("POST", "/api/v1/users", map[string]any{"username": "template-a", "password": "test-password"}, 201)
	b := c.do("POST", "/api/v1/users", map[string]any{"username": "template-b", "password": "test-password"}, 201)
	aID, bID := int64(a["id"].(float64)), int64(b["id"].(float64))
	aKey, _ := issueTestMemberSession(t, c, aID, "read", "write")
	bKey, _ := issueTestMemberSession(t, c, bID, "read", "write")
	readOnly, _ := issueTestAPIKey(t, c, aID, "read")
	adminKey, _ := issueTestAPIKey(t, c, 1, "read", "write")
	ctx := context.Background()
	sh := domain.Share{Name: "personal-api", UserID: &aID}
	if err := c.api.Store.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Name: sh.Name, Kind: domain.SubShare, ShareID: &sh.ID, TokenHash: "personal-api-token", Enabled: true}
	if err := c.api.Store.CreateSubscription(ctx, &sub); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"name": "private", "kind": "mihomo", "content": "mixed-port: 18080\nproxies: []\nproxy-groups: []\nrules: []\n"}
	keyRequest(t, c, "POST", "/api/v1/me/templates", readOnly, body, nil, 401)
	created, _ := keyRequest(t, c, "POST", "/api/v1/me/templates", aKey, body, nil, 201)
	templateID := int64(created["id"].(float64))
	templatePath := fmt.Sprintf("/api/v1/me/templates/%d", templateID)
	keyRequest(t, c, "GET", templatePath, bKey, nil, nil, 404)
	keyRequest(t, c, "PUT", templatePath, bKey, body, nil, 404)
	updatedBody := map[string]any{"name": "private updated", "kind": "mihomo", "content": body["content"]}
	keyRequest(t, c, "PUT", templatePath, aKey, updatedBody, nil, 200)
	keyRequest(t, c, "GET", templatePath+"/versions", bKey, nil, nil, 404)
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/templates/%d/versions", templateID), aKey, nil, nil, 404)
	_, versionsBody := keyRequest(t, c, "GET", templatePath+"/versions", aKey, nil, nil, 200)
	if !strings.Contains(string(versionsBody), "private") || !strings.Contains(string(versionsBody), "18080") {
		t.Fatal("prior personal version missing")
	}
	sharePath := fmt.Sprintf("/api/v1/shares/%d/personal-templates", sh.ID)
	draft := map[string]any{"content": body["content"]}
	keyRequest(t, c, "POST", sharePath+"/preview-draft", bKey, draft, nil, 403)
	keyRequest(t, c, "POST", sharePath+"/preview-draft", readOnly, draft, nil, 401)
	adminDraftPath := fmt.Sprintf("/api/v1/shares/%d/template-preview-draft", sh.ID)
	keyRequest(t, c, "POST", adminDraftPath, aKey, draft, nil, 403)
	keyRequest(t, c, "POST", adminDraftPath, adminKey, draft, nil, 200)
	draftPreview, _ := keyRequest(t, c, "POST", sharePath+"/preview-draft", aKey, draft, nil, 200)
	if !strings.Contains(draftPreview["body"].(string), "18080") {
		t.Fatal("draft preview did not use supplied content")
	}
	if _, ok := draftPreview["groups"].([]any); !ok {
		t.Fatal("draft group diagnostics missing")
	}
	if _, ok := draftPreview["nodes"].([]any); !ok {
		t.Fatal("draft node diagnostics missing")
	}
	warningPreview, _ := keyRequest(t, c, "POST", sharePath+"/preview-draft", aKey, map[string]any{"content": "proxy-groups: []\nrules: ['DOMAIN-SUFFIX,example.test,unknown-node']\n"}, nil, 200)
	if warnings, ok := warningPreview["warnings"].([]any); !ok || len(warnings) != 1 {
		t.Fatal("missing unknown policy diagnostic")
	}
	selection := map[string]any{"template_id": templateID, "format": "mihomo"}
	keyRequest(t, c, "POST", sharePath+"/preview", bKey, selection, nil, 403)
	preview, _ := keyRequest(t, c, "POST", sharePath+"/preview", aKey, selection, nil, 200)
	if !strings.Contains(preview["body"].(string), "18080") {
		t.Fatal("preview did not use personal template")
	}
	keyRequest(t, c, "PUT", sharePath+"/surge", aKey, selection, nil, 400)
	keyRequest(t, c, "PUT", sharePath+"/mihomo", aKey, selection, nil, 200)
	keyRequest(t, c, "PUT", templatePath, aKey, body, nil, 409)
	selected, _ := keyRequest(t, c, "GET", sharePath, aKey, nil, nil, 200)
	if selected["mihomo"] != float64(templateID) {
		t.Fatal("selection missing")
	}
	sh.UserID = &bID
	if err := c.api.Store.UpdateShareSettings(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	keyRequest(t, c, "GET", sharePath, aKey, nil, nil, 403)
	selected, _ = keyRequest(t, c, "GET", sharePath, bKey, nil, nil, 200)
	if len(selected) != 0 {
		t.Fatal("transferred share retained previous user's template")
	}
	keyRequest(t, c, "PUT", sharePath+"/mihomo", bKey, selection, nil, 404)
}
