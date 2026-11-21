package api

import (
	"context"
	"fmt"
	"testing"

	"ctlvps/internal/domain"
)

func TestShareSubscriptionAuthorizationTracksLiveShareOwner(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	a := c.do("POST", "/api/v1/users", map[string]any{"username": "first-member", "password": "test-password"}, 201)
	b := c.do("POST", "/api/v1/users", map[string]any{"username": "second-member", "password": "test-password"}, 201)
	aID, bID := int64(a["id"].(float64)), int64(b["id"].(float64))
	aKey, _ := issueTestMemberSession(t, c, aID, "read")
	bKey, _ := issueTestMemberSession(t, c, bID, "read")
	ctx := context.Background()
	sh := domain.Share{Name: "ownership", UserID: &aID}
	if err := c.api.Store.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Name: "ownership", Kind: domain.SubShare, ShareID: &sh.ID, OwnerUserID: aID, AllowedUserIDs: []int64{aID}, TokenHash: "test-transfer", Enabled: true}
	if err := c.api.Store.CreateSubscription(ctx, &sub); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/subscriptions/%d", sub.ID)
	keyRequest(t, c, "GET", path, aKey, nil, nil, 200)
	keyRequest(t, c, "GET", path, bKey, nil, nil, 403)
	sh.UserID = &bID
	if err := c.api.Store.UpdateShareSettings(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	// Deliberately leave the subscription's cached owner and allowlist stale.
	keyRequest(t, c, "GET", path, aKey, nil, nil, 403)
	keyRequest(t, c, "GET", path, bKey, nil, nil, 200)
	sh.UserID = nil
	if err := c.api.Store.UpdateShareSettings(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	keyRequest(t, c, "GET", path, aKey, nil, nil, 403)
	keyRequest(t, c, "GET", path, bKey, nil, nil, 403)
}
