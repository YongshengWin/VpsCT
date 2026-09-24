package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"ctlvps/internal/networkconfig"
)

// Tests explicitly preview where a user would review; c.do deliberately does
// not inject a token, so missing-preview and stale-snapshot tests remain real.
func reviewNodeBody(c *client, path string, body map[string]any) map[string]any {
	request := map[string]any{"network": body["network"]}
	if host, ok := body["advertise_host"]; ok {
		request["advertise_host"] = host
	}
	view := c.do("POST", path+"/preview", request, 200)
	body["expected_impact"] = view["impact"].(map[string]any)["token"]
	return view
}
func reviewEgressBody(c *client, path, action string, body map[string]any) map[string]any {
	request := map[string]any{"action": action}
	for _, key := range []string{"expected_revision", "name", "kind", "enabled", "config", "credentials"} {
		if v, ok := body[key]; ok {
			request[key] = v
		}
	}
	view := c.do("POST", path+"/preview", request, 200)
	body["expected_impact"] = view["impact"].(map[string]any)["token"]
	return view
}

func TestEgressReviewRequiredAndNewConsumerRejectsWholeMutation(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	sid := int64(c.do("POST", "/api/v1/servers", map[string]any{"name": "impact fixture"}, 201)["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	path := base + "/egress-profiles"
	body := map[string]any{"operation_id": strings.Repeat("1", 32), "expected_revision": 0, "name": "direct", "kind": "direct", "enabled": true, "config": networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}}
	c.do("POST", path, body, 428)
	reviewEgressBody(c, path, "create", body)
	pid := int64(c.do("POST", path, body, 202)["resource_id"].(float64))
	path = fmt.Sprintf("/api/v1/egress-profiles/%d", pid)
	body["operation_id"], body["expected_revision"], body["enabled"] = strings.Repeat("2", 32), 1, false
	view := reviewEgressBody(c, path, "update", body)
	if view["impact"].(map[string]any)["runtime_change"] != false {
		t.Fatal(view)
	}
	nid := int64(c.do("POST", base+"/nodes", map[string]any{"protocol": "ss", "port": 21101}, 201)["id"].(float64))
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", EgressProfileID: pid, EgressRevision: 1, OnUnavailable: "block"}
	if _, err := c.api.Store.SetNodeNetwork(ctx, nid, 0, policy, nil); err != nil {
		t.Fatal(err)
	}
	if rejected := c.do("PUT", path, body, 409); rejected["error"].(map[string]any)["code"] != "network_impact_changed" {
		t.Fatal(rejected)
	}
	p, err := c.api.Store.GetEgressProfile(ctx, pid)
	if err != nil || !p.Enabled || p.CurrentRevision != 1 {
		t.Fatal("new consumer was stopped", err)
	}
	reviewEgressBody(c, path, "update", body)
	op := c.do("PUT", path, body, 202)
	if replay := c.do("PUT", path, body, 202); replay["id"] != op["id"] {
		t.Fatal("retry required a new preview")
	}
	// The token is bound to this draft, not merely to a server or node list.
	body["operation_id"], body["expected_revision"], body["enabled"] = strings.Repeat("3", 32), 2, true
	reviewEgressBody(c, path, "update", body)
	body["name"] = "unreviewed name"
	c.do("PUT", path, body, 409)
}
