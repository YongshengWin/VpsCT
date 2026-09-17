package api

import (
	"bytes"
	"context"
	"testing"

	"ctlvps/internal/domain"
)

func TestBulkRegenerateNodes(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/nodes/bulk-regenerate", map[string]any{"ids": []int64{1}}, 401)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	c.do("POST", "/api/v1/nodes/bulk-regenerate", map[string]any{"ids": []int64{}}, 400)
	c.do("POST", "/api/v1/nodes/bulk-regenerate", map[string]any{"ids": make([]int64, 501)}, 400)
	ctx := context.Background()
	st := c.api.Store
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "test-server", "public_host": "test.example"}, 201)
	sid := int64(srv["id"].(float64))
	first := c.do("POST", "/api/v1/servers/"+itoa(srv["id"])+"/nodes", map[string]any{"protocol": "vless", "name": "first"}, 201)
	second := c.do("POST", "/api/v1/servers/"+itoa(srv["id"])+"/nodes", map[string]any{"protocol": "vless", "name": "second"}, 201)
	n1, _ := st.GetNode(ctx, int64(first["id"].(float64)))
	n2, _ := st.GetNode(ctx, int64(second["id"].(float64)))
	n2.Enabled = false
	if err := st.UpdateNode(ctx, &n2); err != nil {
		t.Fatal(err)
	}
	add := func(n domain.Node) domain.Node {
		t.Helper()
		if err := st.CreateNode(ctx, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	manual := add(domain.Node{Name: "manual", Source: domain.NodeManual})
	imported := add(domain.Node{Name: "imported", Source: domain.NodeImported})
	revoked := n1
	revoked.ID = 0
	revoked.Name = "revoked"
	revoked.Revoked = true
	revoked = add(revoked)
	chain := add(domain.Node{Name: "chain", Source: domain.NodeChain, Server: n1.Server, Port: n1.Port, Protocol: n1.Protocol, Params: n1.Params, ChainFrontNodeID: &n2.ID})
	sub := c.do("POST", "/api/v1/subscriptions", map[string]any{"name": "stable", "kind": "generated", "node_selection": map[string]any{"include_all": true}}, 201)
	oldSub, _ := st.GetSubscription(ctx, int64(sub["id"].(float64)))
	before, _ := st.LatestDesiredState(ctx, sid)
	out := c.do("POST", "/api/v1/nodes/bulk-regenerate", map[string]any{"ids": []int64{n1.ID, n2.ID, n1.ID, manual.ID, imported.ID, revoked.ID, chain.ID, 999999}}, 200)
	if out["rotated"] != float64(2) {
		t.Fatalf("rotated: %v", out)
	}
	statuses := map[string]int{}
	for _, row := range out["results"].([]any) {
		statuses[row.(map[string]any)["status"].(string)]++
	}
	if statuses["rotated"] != 2 || statuses["skipped"] != 4 || statuses["failed"] != 1 {
		t.Fatalf("statuses: %v", statuses)
	}
	servers := out["servers"].([]any)
	if len(servers) != 1 || servers[0].(map[string]any)["published"] != true {
		t.Fatalf("servers: %v", servers)
	}
	after, _ := st.LatestDesiredState(ctx, sid)
	if after.Revision != before.Revision+1 {
		t.Fatal("must publish once per server")
	}
	for _, old := range []domain.Node{n1, n2} {
		got, err := st.GetNode(ctx, old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(got.Params, old.Params) {
			t.Fatal("credentials did not change")
		}
		if got.Name != old.Name || got.Port != old.Port || got.Enabled != old.Enabled {
			t.Fatal("rotation changed node identity/state")
		}
	}
	newSub, _ := st.GetSubscription(ctx, oldSub.ID)
	if newSub.Token != oldSub.Token || newSub.ShortCode != oldSub.ShortCode {
		t.Fatal("subscription links changed")
	}
	newRevoked, _ := st.GetNode(ctx, revoked.ID)
	if !bytes.Equal(newRevoked.Params, revoked.Params) || !newRevoked.Revoked {
		t.Fatal("revoked node changed")
	}
	newChain, _ := st.GetNode(ctx, chain.ID)
	newFirst, _ := st.GetNode(ctx, n1.ID)
	if !bytes.Equal(newChain.Params, newFirst.Params) {
		t.Fatal("chain copy retained old credentials")
	}
}

func TestBulkRegenerateReportsPublishFailure(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "test-server", "public_host": "test.example"}, 201)
	node := c.do("POST", "/api/v1/servers/"+itoa(srv["id"])+"/nodes", map[string]any{"protocol": "vless", "name": "first"}, 201)
	if _, err := c.api.Store.DB().Exec(`CREATE TRIGGER fail_publish BEFORE INSERT ON desired_states BEGIN SELECT RAISE(FAIL, 'test publish failure'); END`); err != nil {
		t.Fatal(err)
	}
	out := c.do("POST", "/api/v1/nodes/bulk-regenerate", map[string]any{"ids": []any{node["id"]}}, 200)
	if out["rotated"] != float64(1) || out["servers"].([]any)[0].(map[string]any)["published"] != false {
		t.Fatalf("publish failure must be visible: %v", out)
	}
	ctx := context.Background()
	beforeRetry, _ := c.api.Store.GetNode(ctx, int64(node["id"].(float64)))
	if _, err := c.api.Store.DB().Exec(`DROP TRIGGER fail_publish`); err != nil {
		t.Fatal(err)
	}
	c.do("POST", "/api/v1/servers/"+itoa(srv["id"])+"/republish", nil, 200)
	afterRetry, _ := c.api.Store.GetNode(ctx, beforeRetry.ID)
	if !bytes.Equal(beforeRetry.Params, afterRetry.Params) {
		t.Fatal("republishing must not rotate credentials again")
	}
}
