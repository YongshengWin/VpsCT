package api

import "testing"

func TestNormalUserCannotReceiveTwoShares(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "one-share-user", "password": "test-password"}, 201)
	body := map[string]any{"name": "first", "user_id": member["id"], "targets": []any{}}
	c.do("POST", "/api/v1/shares", body, 201)
	body["name"] = "second"
	c.do("POST", "/api/v1/shares", body, 409)
}
