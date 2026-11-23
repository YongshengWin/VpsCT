package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

func TestVLESSDeploymentProfilesAndDowngradeFloor(t *testing.T) {
	c, id := natAPIClient(t)
	ctx := t.Context()
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	readOnly, _ := issueTestAPIKey(t, c, 1, "read")
	base := fmt.Sprintf("/api/v1/servers/%d", id)
	endpoint := base + "/nodes"
	create := func(body map[string]any, want int) map[string]any {
		t.Helper()
		n, _ := keyRequest(t, c, "POST", endpoint, key, body, nil, want)
		return n
	}
	desired := func(cap string, checkStatus int) agentproto.DesiredState {
		t.Helper()
		req, _ := http.NewRequest("GET", c.srv.URL+"/api/agent/v1/desired", nil)
		req.Header.Set("Authorization", "Bearer "+c.agent)
		if cap != "" {
			req.Header.Set(agentproto.XrayHeader, cap)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != checkStatus {
			t.Fatalf("desired with capability %q: status %d want %d", cap, res.StatusCode, checkStatus)
		}
		var d agentproto.DesiredState
		if checkStatus == 200 {
			if err = json.NewDecoder(res.Body).Decode(&d); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	combo := map[string]any{"transport": "xhttp", "security": "reality", "xhttp": map[string]string{"path": "/node", "host": "front.example.org", "mode": "packet-up"}}
	body := map[string]any{"protocol": "vless", "sni": "origin.example.org", "vless": combo, "port": 41001}
	legacy := create(map[string]any{"protocol": "vless", "port": 41000}, 201)
	if legacy["core"] != "singbox" || legacy["params"].(map[string]any)["flow"] != "xtls-rprx-vision" {
		t.Fatal("legacy entry changed")
	}
	desired("", 200)
	keyRequest(t, c, "POST", endpoint, readOnly, body, nil, 403)
	keyRequest(t, c, "POST", endpoint, "invalid-fixture-key", body, nil, 401)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "member", "password": rand.Text(), "role": "user", "enabled": true}, 201)
	memberSession, _ := issueTestMemberSession(t, c, int64(member["id"].(float64)))
	keyRequest(t, c, "POST", endpoint, memberSession, body, nil, 403)
	for _, bad := range []map[string]any{{"protocol": "ss", "vless": combo}, {"protocol": "vless", "vless": map[string]any{"transport": "xhttp", "flow": "xtls-rprx-vision"}}, {"protocol": "vless", "vless": map[string]any{"transport": "tcp", "xhttp": map[string]any{}}}, {"protocol": "vless", "vless": map[string]any{"transport": "ws"}}, {"protocol": "vless", "vless": map[string]any{"transport": "xhttp", "xhttp": map[string]any{"extra": map[string]any{"mode": "anything"}}}}} {
		create(bad, 400)
	}
	if msg := create(body, 409)["error"].(map[string]any)["code"]; msg != "xhttp_unavailable" {
		t.Fatal(msg)
	}
	hb := agentproto.Heartbeat{Version: "fixture", Metrics: agentproto.Metrics{Arch: "amd64"}, Diagnostics: agentproto.Diagnostics{XrayVersion: 1, SecurityVersion: 1, SecurityPolicy: true, Systemd: true, Nftables: true}}
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	v, _ := keyRequest(t, c, "GET", base, key, nil, nil, 200)
	if v["xhttp_block_reason"] != "" {
		t.Fatal("capable host blocked")
	}
	n := create(body, 201)
	nid := int64(n["id"].(float64))
	params := n["params"].(map[string]any)
	if n["core"] != "xray" || params["network"] != "xhttp" || params["flow"] != nil || params["servername"] != "origin.example.org" || params["xhttp-opts"].(map[string]any)["host"] != "front.example.org" || params["reality_private_key"] != nil {
		t.Fatal("XHTTP client projection invalid")
	}
	desired("", 409)
	desired("2", 409)
	d := desired("1", 200)
	if d.XrayVersion != 1 || len(d.Nodes) != 2 {
		t.Fatal("desired lost one of two VLESS transports")
	}
	for _, node := range d.Nodes {
		if node.NodeID == nid {
			if err := agentproto.ValidateXrayNode(&d, node); err != nil {
				t.Fatal(err)
			}
		}
	}
	stored, err := c.api.Store.GetNode(ctx, nid)
	if err != nil {
		t.Fatal(err)
	}
	if _, ready, _ := c.api.Store.AppliedSubscriptionNode(ctx, stored); ready {
		t.Fatal("published before application")
	}
	for _, change := range []map[string]any{{"name": "unchanged", "core_mode": "lean"}, {"name": "unchanged", "is_nat": true}} {
		conflict, _ := keyRequest(t, c, "PUT", base, key, change, nil, 409)
		e := conflict["error"].(map[string]any)
		if e["code"] != "node_combination_conflict" || !strings.Contains(e["message"].(string), fmt.Sprint(nid)) {
			t.Fatal("conflict must identify affected node")
		}
	}
	s, _ := c.api.Store.GetServer(ctx, id)
	if s.CoreMode != domain.CoreModeStable || s.IsNAT {
		t.Fatal("rejected edit mutated server")
	}
	path := fmt.Sprintf("/api/v1/nodes/%d", nid)
	c.do("POST", base+"/reset-token", nil, 204)
	et := c.do("POST", base+"/enroll-token", nil, 200)
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string), Version: "replacement", Arch: "amd64"}, 200)
	c.agent = en["agent_token"].(string)
	keyRequest(t, c, "POST", path+"/regenerate", key, nil, nil, 409) // no new heartbeat; old capability cannot follow the token
	desired("", 409)
	hb.Diagnostics.XrayVersion = 0
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	keyRequest(t, c, "POST", path+"/regenerate", key, nil, nil, 409)
	unchanged, _ := c.api.Store.GetNode(ctx, nid)
	if string(unchanged.Params) != string(stored.Params) {
		t.Fatal("downgrade rotated credentials")
	}
	keyRequest(t, c, "DELETE", path, key, nil, nil, 204)
	desired("", 409)
	d = desired("1", 200)
	if d.XrayVersion != 1 || len(d.Nodes) != 1 || d.Nodes[0].Core != "singbox" {
		t.Fatal("cleanup floor or original node lost")
	}
}
