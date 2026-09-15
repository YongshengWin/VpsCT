package api

import (
	"context"
	"fmt"
	"testing"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
)

func TestUploadedSubscriptionInputsAreRejected(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"username": "admin", "password": "test-password", "setup_token": testSetupToken}, 200)
	for _, body := range []map[string]any{
		{"name": "removed type", "kind": "uploaded"},
		{"name": "removed content", "kind": "generated", "uploaded_content": "proxies: []"},
	} {
		c.do("POST", "/api/v1/subscriptions", body, 400)
		c.do("POST", "/api/v1/subscriptions/preview", body, 400)
	}
	sub := c.do("POST", "/api/v1/subscriptions", map[string]any{"name": "generated", "kind": "generated"}, 201)
	path := fmt.Sprintf("/api/v1/subscriptions/%v", sub["id"])
	c.do("PUT", path, map[string]any{"uploaded_content": "proxies: []"}, 400)
	c.do("PUT", path, map[string]any{"kind": "uploaded"}, 400)
	c.do("GET", path+"/render?format=mihomo", nil, 200)
}

func TestLegacyUploadedSubscriptionIsRetiredWithoutDeletingData(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"username": "admin", "password": "test-password", "setup_token": testSetupToken}, 200)
	ctx := context.Background()
	token := auth.NewSubscriptionToken()
	legacy := domain.Subscription{
		Name: "legacy configuration", Kind: "uploaded", Enabled: true,
		Token: token, TokenHash: auth.HashToken(token), ShortCode: auth.NewSubscriptionToken(),
		NodeSelection: domain.NodeSelection{IncludeAll: true},
	}
	if err := c.api.Store.CreateSubscription(ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	const original = "proxies: []\nrules: [MATCH,DIRECT]\n"
	if _, err := c.api.Store.DB().ExecContext(ctx, `UPDATE subscriptions SET uploaded_content=? WHERE id=?`, original, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.api.Subs.Build(ctx, legacy); err == nil {
		t.Fatal("legacy kind must not fall through to generated rendering")
	}
	path := fmt.Sprintf("/api/v1/subscriptions/%d", legacy.ID)
	view := c.do("GET", path, nil, 200)
	if view["supported"] != false || view["enabled"] != false || view["token"] != "" || len(view["links"].(map[string]any)) != 0 {
		t.Fatal("retired subscription must be visible as disabled without usable links")
	}
	if _, exists := view["uploaded_content"]; exists {
		t.Fatal("legacy content must not be returned by the API")
	}
	list := c.do("GET", "/api/v1/subscriptions", nil, 200)["list"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["supported"] != false {
		t.Fatal("legacy record should remain available for explicit deletion")
	}
	c.do("GET", "/s/"+token+"/mihomo", nil, 410)
	c.do("GET", "/r/"+legacy.ShortCode, nil, 410)
	c.do("GET", path+"/render?format=mihomo", nil, 400)
	c.do("PUT", path, map[string]any{"enabled": true}, 410)
	c.do("POST", path+"/rotate-token", nil, 410)
	var content string
	if err := c.api.Store.DB().QueryRowContext(ctx, `SELECT uploaded_content FROM subscriptions WHERE id=?`, legacy.ID).Scan(&content); err != nil || content != original {
		t.Fatal("retiring this feature must preserve the stored legacy content")
	}
	c.do("DELETE", path, nil, 204)
}
