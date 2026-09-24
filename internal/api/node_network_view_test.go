package api

import (
	"context"
	"fmt"
	"testing"

	"ctlvps/internal/networkconfig"
)

func TestNodeNetworkSurvivesOldAPIUpdatesAndIsHiddenFromShareMembers(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	server := c.do("POST", "/api/v1/servers", map[string]any{"name": "network-api", "public_host": "server.example.test"}, 201)
	sid := int64(server["id"].(float64))
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "binding-member", "password": "password123", "role": "user", "enabled": true}, 201)
	share := c.do("POST", "/api/v1/shares", map[string]any{"name": "binding-share", "user_id": member["id"], "targets": []any{map[string]any{"server_id": sid, "protocols": []string{"ss"}}}}, 201)
	shareID := int64(share["id"].(float64))
	nodeID := int64(share["nodes"].([]any)[0].(map[string]any)["id"].(float64))
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "override", OnUnavailable: "block"}
	host := "override.example.test"
	if _, err := c.api.Store.SetNodeNetwork(context.Background(), nodeID, 0, policy, &host); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/v1/nodes/%d", nodeID)
	view := c.do("GET", base, nil, 200)
	if view["network"] == nil || view["network_revision"] != float64(1) {
		t.Fatal("admin cannot inspect binding")
	}
	c.do("PUT", base, map[string]any{"name": "renamed by old form"}, 200)
	c.do("POST", base+"/regenerate", nil, 200)
	c.do("PUT", fmt.Sprintf("/api/v1/servers/%d", sid), map[string]any{"name": "network-api", "public_host": "changed.example.test"}, 200)
	got, err := c.api.Store.GetNode(context.Background(), nodeID)
	if err != nil || got.Network == nil || got.NetworkRevision != 1 || got.Server != host {
		t.Fatal("old API overwrote independent binding", err)
	}
	c.cookie = nil
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "binding-member", "password": "password123"}, 200)
	owned := c.do("GET", fmt.Sprintf("/api/v1/shares/%d", shareID), nil, 200)
	nodes := owned["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatal("owned share node missing")
	}
	for _, entry := range nodes {
		n := entry.(map[string]any)
		if n["network"] != nil || n["network_revision"] != nil {
			t.Fatal("member saw server-side network policy")
		}
		if n["server"] != host || n["uri"] == "" {
			t.Fatal("member lost client endpoint")
		}
	}
}
