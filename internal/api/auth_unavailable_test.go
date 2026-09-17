package api

import "testing"

func TestAuthDatabaseFailureIsNotBadCredentials(t *testing.T) {
	c := newTestAPI(t)
	credentials := map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}
	c.do("POST", "/api/v1/auth/setup", credentials, 200)
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "wrong"}, 401)
	if err := c.api.Store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/dashboard", "/api/v1/auth/login"} {
		method := "GET"
		var body any
		if path == "/api/v1/auth/login" {
			method, body = "POST", map[string]any{"username": "admin", "password": "password123"}
		}
		response := c.do(method, path, body, 503)
		if response["error"].(map[string]any)["code"] != "auth_unavailable" {
			t.Fatalf("%s: incorrect error classification", path)
		}
	}
}

func TestAgentDatabaseFailureIsNotRevokedToken(t *testing.T) {
	c := newTestAPI(t)
	c.agent = "test-invalid-token"
	c.do("GET", "/api/agent/v1/desired", nil, 401)
	if err := c.api.Store.Close(); err != nil {
		t.Fatal(err)
	}
	response := c.do("GET", "/api/agent/v1/desired", nil, 503)
	if response["error"].(map[string]any)["code"] != "auth_unavailable" {
		t.Fatal("database error misclassified as invalid token")
	}
}
