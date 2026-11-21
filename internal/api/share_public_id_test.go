package api

import (
	"context"
	"fmt"
	"testing"

	"ctlvps/internal/domain"
)

func TestPublicShareIDRequiresAuthorization(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	a := c.do("POST", "/api/v1/users", map[string]any{"username": "share-a", "password": "test-password"}, 201)
	b := c.do("POST", "/api/v1/users", map[string]any{"username": "share-b", "password": "test-password"}, 201)
	aID, bID := int64(a["id"].(float64)), int64(b["id"].(float64))
	aKey, _ := issueTestMemberSession(t, c, aID, "read")
	bKey, _ := issueTestMemberSession(t, c, bID, "read")
	admin, _ := issueTestAPIKey(t, c, 1, "read")
	sh := domain.Share{Name: "fixture", UserID: &aID}
	if err := c.api.Store.CreateShare(context.Background(), &sh); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/share-pages/" + sh.PublicID
	keyRequest(t, c, "GET", path, "", nil, nil, 401)
	keyRequest(t, c, "GET", path, bKey, nil, nil, 404)
	keyRequest(t, c, "GET", "/api/v1/share-pages/s_invalid", aKey, nil, nil, 404)
	for _, key := range []string{aKey, admin} {
		got, _ := keyRequest(t, c, "GET", path, key, nil, nil, 200)
		if got["public_id"] != sh.PublicID || got["id"] != float64(sh.ID) {
			t.Fatal("wrong share returned")
		}
	}
	keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/shares/%d", sh.ID), aKey, nil, nil, 200)
	sh.UserID = &bID
	if err := c.api.Store.UpdateShareSettings(context.Background(), &sh); err != nil {
		t.Fatal(err)
	}
	keyRequest(t, c, "GET", path, aKey, nil, nil, 404)
	keyRequest(t, c, "GET", path, bKey, nil, nil, 200)
}
