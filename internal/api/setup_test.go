package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetupCapability(t *testing.T) {
	c := newTestAPI(t)
	body := map[string]any{"username": "admin", "password": "test-password-123"}
	c.do("POST", "/api/v1/auth/setup", body, 403)
	body["setup_token"] = "wrong"
	c.do("POST", "/api/v1/auth/setup", body, 403)
	status := c.do("GET", "/api/v1/auth/setup", nil, 200)
	if status["needs_setup"] != true || status["setup_token"] != nil {
		t.Fatal("setup status must not disclose the capability")
	}
	path := filepath.Join(c.api.Config.DataDir, "setup-token")
	if err := os.WriteFile(path, []byte(testSetupToken), 0o600); err != nil {
		t.Fatal(err)
	}
	body["setup_token"] = testSetupToken
	c.do("POST", "/api/v1/auth/setup", body, 200)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("spent token file was not removed")
	}
	body["username"] = "second-admin"
	c.do("POST", "/api/v1/auth/setup", body, 409)
}

func TestSetupWithoutConfiguredTokenFailsClosed(t *testing.T) {
	c := newTestAPI(t)
	c.api.Config.SetupToken = ""
	c.do("POST", "/api/v1/auth/setup", map[string]any{"username": "admin", "password": "test-password-123", "setup_token": ""}, 403)
}
