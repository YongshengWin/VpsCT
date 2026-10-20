package api

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/store"
)

func TestImportedNodeRenameSurvivesSync(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	ctx := context.Background()
	ext := domain.ExternalSubscription{Name: "fixture", URL: "https://example.com/sub", Enabled: true}
	if err := c.api.Store.CreateExternal(ctx, &ext); err != nil {
		t.Fatal(err)
	}
	fresh := func(port int) []domain.Node {
		return []domain.Node{
			{Name: "upstream-a", Protocol: "ss", Server: "example.com", Port: port, Params: []byte(`{"cipher":"aes-128-gcm","password":"fixture"}`)},
			{Name: "upstream-b", Protocol: "ss", Server: "example.org", Port: 8388},
		}
	}
	if _, _, _, err := c.api.Store.ReplaceExternalNodes(ctx, ext.ID, fresh(8388)); err != nil {
		t.Fatal(err)
	}
	nodes, err := c.api.Store.ListNodes(ctx, store.NodeFilter{ExternalSubID: &ext.ID})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(nodes[0].ID, 10)
	// A local name may equal another upstream name without changing either identity.
	for _, name := range []string{"upstream-b", "  自定义节点  "} {
		got := c.do("PUT", "/api/v1/nodes/"+id, map[string]any{"name": name}, 200)
		if got["name"] != strings.TrimSpace(name) || got["original_name"] != "upstream-a" {
			t.Fatalf("rename: %v", got)
		}
		added, updated, removed, err := c.api.Store.ReplaceExternalNodes(ctx, ext.ID, fresh(9443))
		if err != nil || added != 0 || updated != 2 || removed != 0 {
			t.Fatalf("sync: %d %d %d %v", added, updated, removed, err)
		}
		got = c.do("GET", "/api/v1/nodes/"+id, nil, 200)
		if got["name"] != strings.TrimSpace(name) || got["port"] != float64(9443) {
			t.Fatalf("sync lost name or connection update: %v", got)
		}
	}
	c.do("PUT", "/api/v1/nodes/"+id, map[string]any{"name": "  "}, 400)
	sub := c.do("POST", "/api/v1/subscriptions", map[string]any{"name": "fixture", "kind": "generated", "node_selection": map[string]any{"include_all": true}}, 201)
	rendered := c.do("GET", "/api/v1/subscriptions/"+itoa(sub["id"])+"/render?format=mihomo", nil, 200)
	if !strings.Contains(rendered["body"].(string), "自定义节点") {
		t.Fatal("subscription did not use custom name")
	}
	// Upstream removal still removes the renamed node.
	_, _, removed, err := c.api.Store.ReplaceExternalNodes(ctx, ext.ID, fresh(8388)[1:])
	if err != nil || removed != 1 {
		t.Fatalf("remove renamed node: %d %v", removed, err)
	}
	c.do("GET", "/api/v1/nodes/"+id, nil, 404)
}
