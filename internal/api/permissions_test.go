package api

import (
	"context"
	"fmt"
	"testing"

	"ctlvps/internal/domain"
)

func TestPermissionGrantRequiresReauthenticationAndInvalidatesKey(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "permission-member", "password": "test-password"}, 201)
	id := int64(member["id"].(float64))
	adminKey, _ := issueTestAPIKey(t, c, 1, "read", "write")
	memberKey, _ := issueTestMemberSession(t, c, id, "read", "write")
	path := fmt.Sprintf("/api/v1/users/%d/permissions/templates.create", id)
	body := map[string]any{"scope": map[string]any{"kind": "all"}, "password": "test-password"}
	keyRequest(t, c, "PUT", path, memberKey, body, nil, 403)
	keyRequest(t, c, "PUT", path, adminKey, map[string]any{"scope": map[string]any{"kind": "all"}, "password": "wrong"}, nil, 403)
	grant, _ := keyRequest(t, c, "PUT", path, adminKey, body, nil, 200)
	keyRequest(t, c, "GET", "/api/v1/me/permissions", memberKey, nil, nil, 401)
	memberKey, _ = issueTestMemberSession(t, c, id, "read", "write")
	me, _ := keyRequest(t, c, "GET", "/api/v1/me/permissions", memberKey, nil, nil, 200)
	grants := me["grants"].([]any)
	if len(grants) != 1 || grants[0].(map[string]any)["effective"] != true {
		t.Fatal("effective grant not visible")
	}
	grantID := int64(grant["id"].(float64))
	keyRequest(t, c, "DELETE", fmt.Sprintf("/api/v1/permissions/%d", grantID), memberKey, body, nil, 403)
	keyRequest(t, c, "DELETE", fmt.Sprintf("/api/v1/permissions/%d", grantID), adminKey, map[string]any{"password": "test-password"}, nil, 204)
	keyRequest(t, c, "GET", "/api/v1/me/permissions", memberKey, nil, nil, 401)
}

func TestDelegatedTemplatePermissionDoesNotExpandOtherModules(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	manager := c.do("POST", "/api/v1/users", map[string]any{"username": "template-manager", "password": "test-password"}, 201)
	other := c.do("POST", "/api/v1/users", map[string]any{"username": "template-owner", "password": "test-password"}, 201)
	managerID, otherID := int64(manager["id"].(float64)), int64(other["id"].(float64))
	adminKey, _ := issueTestAPIKey(t, c, 1, "read", "write")
	otherKey, _ := issueTestMemberSession(t, c, otherID, "read", "write")
	input := map[string]any{"name": "example", "kind": "mihomo", "content": "proxies: []\nproxy-groups: []\nrules: []\n"}
	private, _ := keyRequest(t, c, "POST", "/api/v1/me/templates", otherKey, input, nil, 201)
	for _, perm := range []string{"templates.create", "templates.edit"} {
		keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/users/%d/permissions/%s", managerID, perm), adminKey, map[string]any{"scope": map[string]any{"kind": "all"}, "password": "test-password"}, nil, 200)
	}
	key, _ := issueTestMemberSession(t, c, managerID, "read", "write")
	read, _ := issueTestAPIKey(t, c, managerID, "read")
	keyRequest(t, c, "POST", "/api/v1/templates", read, input, nil, 401)
	created, _ := keyRequest(t, c, "POST", "/api/v1/templates", key, input, nil, 201)
	keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/templates/%d", int64(created["id"].(float64))), key, input, nil, 200)
	keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/templates/%d", int64(private["id"].(float64))), key, input, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/servers", key, nil, nil, 403)
	keyRequest(t, c, "DELETE", fmt.Sprintf("/api/v1/templates/%d", int64(created["id"].(float64))), key, nil, nil, 403)
}

func TestPermissionBatchIsAtomicAndRequiresOneReauthentication(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "batch-member", "password": "test-password"}, 201)
	id := int64(member["id"].(float64))
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	path := fmt.Sprintf("/api/v1/users/%d/permissions", id)
	grants := []any{map[string]any{"permission": "templates.create", "scope": map[string]any{"kind": "all"}}, map[string]any{"permission": "templates.edit", "scope": map[string]any{"kind": "all"}}}
	keyRequest(t, c, "POST", path, key, map[string]any{"grants": grants, "password": "test-password"}, nil, 200)
	view, _ := keyRequest(t, c, "GET", path, key, nil, nil, 200)
	if len(view["grants"].([]any)) != 2 {
		t.Fatal("batch did not save both grants")
	}
	invalid := []any{grants[0], map[string]any{"permission": "servers.maintenance", "scope": map[string]any{"kind": "all"}}}
	keyRequest(t, c, "POST", path, key, map[string]any{"grants": invalid, "password": "test-password"}, nil, 400)
	view, _ = keyRequest(t, c, "GET", path, key, nil, nil, 200)
	if len(view["grants"].([]any)) != 2 {
		t.Fatal("invalid batch changed prior grants")
	}
}

func TestFullAdminPromotionRequiresReauthentication(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	c.do("POST", "/api/v1/users", map[string]any{"username": "new-admin", "password": "test-password", "role": "admin"}, 403)
	admin := c.do("POST", "/api/v1/users", map[string]any{"username": "new-admin", "password": "test-password", "role": "admin", "reauth_password": "test-password"}, 201)
	if admin["role"] != "admin" {
		t.Fatal("administrator role missing")
	}
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "promoted-member", "password": "test-password", "role": "user"}, 201)
	path := fmt.Sprintf("/api/v1/users/%d", int64(member["id"].(float64)))
	c.do("PUT", path, map[string]any{"role": "admin"}, 403)
	c.do("PUT", path, map[string]any{"role": "admin", "reauth_password": "test-password"}, 200)
}

func TestOwnerProtectionAndTransfer(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "owner", "password": "test-password"}, 200)
	other := c.do("POST", "/api/v1/users", map[string]any{"username": "other-admin", "password": "test-password", "role": "admin", "reauth_password": "test-password"}, 201)
	otherID := int64(other["id"].(float64))
	ownerKey, _ := issueTestAPIKey(t, c, 1, "read", "write")
	otherKey, _ := issueTestAPIKey(t, c, otherID, "read", "write")
	keyRequest(t, c, "PUT", "/api/v1/users/1", otherKey, map[string]any{"password": "new-test-password"}, nil, 403)
	keyRequest(t, c, "DELETE", "/api/v1/users/1", otherKey, nil, nil, 403)
	keyRequest(t, c, "POST", "/api/v1/system/owner", otherKey, map[string]any{"user_id": otherID, "password": "test-password"}, nil, 403)
	keyRequest(t, c, "POST", "/api/v1/system/owner", ownerKey, map[string]any{"user_id": otherID, "password": "wrong"}, nil, 403)
	keyRequest(t, c, "POST", "/api/v1/system/owner", ownerKey, map[string]any{"user_id": otherID, "password": "test-password"}, nil, 200)
	keyRequest(t, c, "PUT", fmt.Sprintf("/api/v1/users/%d", otherID), ownerKey, map[string]any{"role": "user"}, nil, 403)
	current, _ := keyRequest(t, c, "GET", "/api/v1/system/owner", otherKey, nil, nil, 200)
	if current["user_id"] != float64(otherID) || current["is_owner"] != true {
		t.Fatal("owner transfer did not persist")
	}
}

func TestDelegatedShareScopeAndSeparateLinksPermission(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	manager := c.do("POST", "/api/v1/users", map[string]any{"username": "share-manager", "password": "test-password"}, 201)
	owner := c.do("POST", "/api/v1/users", map[string]any{"username": "share-owner", "password": "test-password"}, 201)
	managerID, ownerID := int64(manager["id"].(float64)), int64(owner["id"].(float64))
	ctx := context.Background()
	sh := domain.Share{Name: "delegated", UserID: &ownerID, Notes: "private note"}
	other := domain.Share{Name: "outside", UserID: &ownerID}
	for _, s := range []*domain.Share{&sh, &other} {
		if err := c.api.Store.CreateShare(ctx, s); err != nil {
			t.Fatal(err)
		}
		sub := domain.Subscription{Name: s.Name, Kind: domain.SubShare, ShareID: &s.ID, TokenHash: fmt.Sprintf("share-hash-%d", s.ID), Enabled: true}
		if err := c.api.Store.CreateSubscription(ctx, &sub); err != nil {
			t.Fatal(err)
		}
	}
	adminKey, _ := issueTestAPIKey(t, c, 1, "read", "write")
	scope := map[string]any{"kind": "shares", "ids": []int64{sh.ID}}
	grantPath := fmt.Sprintf("/api/v1/users/%d/permissions/", managerID)
	keyRequest(t, c, "PUT", grantPath+"shares.view", adminKey, map[string]any{"scope": scope, "password": "test-password"}, nil, 200)
	key, _ := issueTestMemberSession(t, c, managerID, "read", "write")
	path := fmt.Sprintf("/api/v1/shares/%d", sh.ID)
	view, _ := keyRequest(t, c, "GET", path, key, nil, nil, 200)
	if view["subscription"] != nil || view["nodes"] != nil || view["notes"] != "" {
		t.Fatal("view permission exposed credentials or notes")
	}
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/shares/%d", other.ID), key, nil, nil, 403)
	keyRequest(t, c, "GET", "/api/v1/servers", key, nil, nil, 403)
	keyRequest(t, c, "PUT", grantPath+"shares.links", adminKey, map[string]any{"scope": scope, "password": "test-password"}, nil, 200)
	key, _ = issueTestMemberSession(t, c, managerID, "read", "write")
	view, _ = keyRequest(t, c, "GET", path, key, nil, nil, 200)
	if view["subscription"] == nil {
		t.Fatal("link permission did not expose assigned subscription")
	}
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/shares/%d", other.ID), key, nil, nil, 403)
}

func TestDelegatedShareCreationIsSelfOwned(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	manager := c.do("POST", "/api/v1/users", map[string]any{"username": "share-creator", "password": "test-password"}, 201)
	id := int64(manager["id"].(float64))
	adminKey, _ := issueTestAPIKey(t, c, 1, "read", "write")
	grant := fmt.Sprintf("/api/v1/users/%d/permissions/shares.create", id)
	keyRequest(t, c, "PUT", grant, adminKey, map[string]any{"scope": map[string]any{"kind": "all"}, "password": "test-password"}, nil, 200)
	key, _ := issueTestMemberSession(t, c, id, "read", "write")
	created, _ := keyRequest(t, c, "POST", "/api/v1/shares", key, map[string]any{"name": "self-created", "targets": []any{}}, nil, 201)
	if int64(created["user_id"].(float64)) != id {
		t.Fatal("delegated share was not bound to creator")
	}
	keyRequest(t, c, "POST", "/api/v1/shares", key, map[string]any{"name": "other-owned", "user_id": 1, "targets": []any{}}, nil, 403)
}
