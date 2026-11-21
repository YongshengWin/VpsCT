package api

import (
	"context"
	"fmt"
	"testing"

	"ctlvps/internal/domain"
)

func TestNodeExitRegionBearerPermissions(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "member", "password": "test-password"}, 201)
	admin, _ := issueTestAPIKey(t, c, 1, "read", "write")
	read, _ := issueTestAPIKey(t, c, 1, "read")
	other, _ := issueTestMemberSession(t, c, int64(member["id"].(float64)), "read", "write")
	n := domain.Node{Name: "fixture", Protocol: "ss", Server: "fixture.test", Port: 443, Source: domain.NodeManual, Enabled: true}
	if err := c.api.Store.CreateNode(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/nodes/%d/exit-region", n.ID)
	keyRequest(t, c, "PUT", path, read, map[string]any{"exit_region": "JP"}, nil, 403)
	keyRequest(t, c, "PUT", path, other, map[string]any{"exit_region": "JP"}, nil, 403)
	keyRequest(t, c, "PUT", path, admin, map[string]any{"exit_region": "bad"}, nil, 400)
	keyRequest(t, c, "PUT", path, admin, map[string]any{"exit_region": "jp"}, nil, 200)
	result, _ := keyRequest(t, c, "GET", fmt.Sprintf("/api/v1/nodes/%d", n.ID), admin, nil, nil, 200)
	if result["exit_region"] != "JP" {
		t.Fatal("exit region not persisted")
	}
	keyRequest(t, c, "PUT", path, admin, map[string]any{"exit_region": ""}, nil, 200)
	stored, err := c.api.Store.GetNode(context.Background(), n.ID)
	if err != nil || stored.ExitRegion != "" {
		t.Fatalf("failed to clear region: %v", err)
	}
}
