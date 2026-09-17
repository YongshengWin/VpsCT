package api

import (
	"strings"
	"testing"
)

func TestQuickNodeChain(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	imp := c.do("POST", "/api/v1/nodes/import", map[string]any{"text": "ss://YWVzLTI1Ni1nY206cGFzcw==@vmiss.example:8388#Vmiss\nss://YWVzLTI1Ni1nY206cGFzcw==@zouter.example:8388#Zouter"}, 200)
	created := imp["created"].([]any)
	if len(created) != 2 {
		t.Fatalf("import: %v", imp)
	}
	vmiss := created[0].(map[string]any)
	zouter := created[1].(map[string]any)

	out := c.do("POST", "/api/v1/nodes/chain", map[string]any{"front_id": vmiss["id"], "landing_id": zouter["id"]}, 201)
	if out["name"] != "Vmiss → Zouter" || out["source"] != "chain" || out["chain_front_name"] != "Vmiss" {
		t.Fatalf("new chain node: %v", out)
	}

	list := c.do("GET", "/api/v1/nodes", nil, 200)["list"].([]any)
	var originals, chains int
	for _, row := range list {
		m := row.(map[string]any)
		if m["name"] == "Zouter" {
			originals++
			if m["chain_front_name"] != nil && m["chain_front_name"] != "" {
				t.Fatalf("original landing should stay unchained: %v", m)
			}
		}
		if m["source"] == "chain" {
			chains++
		}
	}
	if originals != 1 || chains != 1 {
		t.Fatalf("want 1 original Zouter and 1 chain, list=%v", list)
	}

	sub := c.do("POST", "/api/v1/subscriptions", map[string]any{
		"name": "chained", "kind": "generated",
		"node_selection": map[string]any{"include_all": true},
		"proxy_groups":   []map[string]any{{"name": "PROXY", "type": "select", "include_all": true}},
	}, 201)
	rendered := c.do("GET", "/api/v1/subscriptions/"+itoa(sub["id"])+"/render?format=mihomo", nil, 200)
	body := rendered["body"].(string)
	if !strings.Contains(body, "Vmiss → Zouter") || !strings.Contains(body, "dialer-proxy: Vmiss") {
		t.Fatalf("mihomo chain missing: %s", body)
	}
	surge := c.do("GET", "/api/v1/subscriptions/"+itoa(sub["id"])+"/render?format=surge", nil, 200)
	sbody := surge["body"].(string)
	if !strings.Contains(sbody, `underlying-proxy="Vmiss"`) {
		t.Fatalf("surge chain missing: %s", sbody)
	}
	if strings.Count(body, "name: Zouter") < 1 {
		t.Fatalf("original Zouter should still be in the subscription: %s", body)
	}

	renamed := c.do("POST", "/api/v1/nodes/chain", map[string]any{"front_id": vmiss["id"], "landing_id": zouter["id"], "name": "  自定义链式  "}, 200)
	if renamed["id"] != out["id"] || renamed["name"] != "自定义链式" {
		t.Fatalf("rename existing chain: %v", renamed)
	}
	unchanged := c.do("POST", "/api/v1/nodes/chain", map[string]any{"front_id": vmiss["id"], "landing_id": zouter["id"]}, 200)
	if unchanged["name"] != "自定义链式" {
		t.Fatalf("omitted name should preserve custom name: %v", unchanged)
	}
	custom := c.do("POST", "/api/v1/nodes/chain", map[string]any{"front_id": zouter["id"], "landing_id": vmiss["id"], "name": "  返程  "}, 201)
	if custom["name"] != "返程" {
		t.Fatalf("custom chain name: %v", custom)
	}

	c.do("POST", "/api/v1/nodes/chain", map[string]any{"front_id": 0, "landing_id": out["id"]}, 204)
}
