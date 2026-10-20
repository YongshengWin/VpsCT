package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
)

// A bearer credential must never promote a caller-selected cookie into a
// browser session, including when the account requires a second login factor.
func TestAPIKeyReviewPasswordDoesNotCreateBrowserSession(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	ctx := context.Background()
	u, err := c.api.Store.GetUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	u.TOTPEnabled, u.TOTPSecret = true, "JBSWY3DPEHPK3PXP"
	if err := c.api.Store.UpdateUser(ctx, &u); err != nil {
		t.Fatal(err)
	}
	u, err = c.api.Store.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain := apiKeyPrefix + auth.RandomToken(32)
	key := domain.APIKey{UserID: u.ID, SecurityVersion: u.SecurityVersion, Name: "review-fixture", TokenHash: auth.HashToken(plain), Scopes: []string{"write"}}
	if err := c.api.Store.CreateAPIKey(ctx, &key); err != nil {
		t.Fatal(err)
	}
	chosenCookie := "review-caller-selected-browser-cookie"
	req, err := http.NewRequest("POST", c.srv.URL+"/api/v1/auth/password", strings.NewReader(`{"old_password":"password123","new_password":"updated-password123"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+plain)
	req.AddCookie(&http.Cookie{Name: c.api.sessionName(), Value: chosenCookie})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusForbidden {
		t.Fatalf("password change response: %d %s", res.StatusCode, body)
	}
	if _, err := c.api.Store.GetSession(ctx, auth.HashToken(chosenCookie)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bearer authentication created a browser session from an unvalidated cookie: %v", err)
	}
	req, err = http.NewRequest("GET", c.srv.URL+"/api/v1/auth/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: c.api.sessionName(), Value: chosenCookie})
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("caller-selected cookie authenticated without two-factor login: %d", res.StatusCode)
	}
}
