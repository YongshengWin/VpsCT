package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestNetworkDesiredNegotiatesPerRequestAndKeepsCleanupContract(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "network-negotiation"}, 201)
	sid := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	et := c.do("POST", base+"/enroll-token", nil, 200)
	en := c.do("POST", agentproto.PathEnroll, agentproto.EnrollRequest{EnrollToken: et["token"].(string)}, 200)
	c.agent = en["agent_token"].(string)
	node := c.do("POST", base+"/nodes", map[string]any{"protocol": "ss", "port": 21001}, 201)
	nid := int64(node["id"].(float64))
	legacy := c.do("GET", agentproto.PathDesired, nil, 200)
	if legacy["network_binding_version"] != nil {
		t.Fatal("unconfigured server changed legacy wire format")
	}
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}
	if _, err := c.api.Store.SetNodeNetwork(ctx, nid, 0, policy, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.api.Desired.Publish(ctx, sid); err != nil {
		t.Fatal(err)
	}
	// A previous capable heartbeat cannot authorize a later old executable.
	c.do("POST", agentproto.PathHeartbeat, agentproto.Heartbeat{Diagnostics: agentproto.Diagnostics{NetworkBindingVersion: 1}}, 200)
	c.do("GET", agentproto.PathDesired, nil, 409)
	fetch := func(version string, count int, status int) {
		t.Helper()
		rec, err := c.api.Store.LatestDesiredState(ctx, sid)
		if err != nil {
			t.Fatal(err)
		}
		path := agentproto.PathDesired
		if status == 409 {
			// Conditional requests cannot bypass the contract using a 304.
			path += fmt.Sprintf("?if_not_revision=%d", rec.Revision)
		}
		req, _ := http.NewRequest("GET", c.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+c.agent)
		req.Header.Set(agentproto.NetworkBindingHeader, version)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatal("unexpected negotiation response", resp.StatusCode, err)
		}
		if status == 409 {
			if strings.Contains(string(raw), `"params"`) {
				t.Fatal("old agent received a partial configuration")
			}
			return
		}
		var ds agentproto.DesiredState
		if err = json.Unmarshal(raw, &ds); err != nil || len(ds.Nodes) != count || ds.NetworkBindingVersion != 1 || ds.Hash != agentproto.ContentHash(&ds) {
			t.Fatal("lost binding contract in published payload", err)
		}
	}
	fetch("", 1, 409)
	fetch("2", 1, 409)
	fetch("1", 1, 200)
	c.do("POST", agentproto.PathHeartbeat, agentproto.Heartbeat{Diagnostics: agentproto.Diagnostics{NetworkBindingErrors: map[int64]string{1: strings.Repeat("x", 513)}}}, 400)
	c.do("DELETE", fmt.Sprintf("/api/v1/nodes/%d", nid), nil, 204)
	fetch("", 0, 409)
	fetch("1", 0, 200)
}

func TestTransitDesiredRejectsDirectOnlyAgentIncludingConditionalCleanup(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "transit-negotiation"}, 201)
	sid := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	et := c.do("POST", base+"/enroll-token", nil, 200)
	en := c.do("POST", agentproto.PathEnroll, agentproto.EnrollRequest{EnrollToken: et["token"].(string)}, 200)
	c.agent = en["agent_token"].(string)
	node := c.do("POST", base+"/nodes", map[string]any{"protocol": "ss", "port": 21001}, 201)
	nid := int64(node["id"].(float64))
	resolver := networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}
	cfg := networkconfig.SOCKS5{Server: "upstream.example.test", ServerPort: 1080, Authentication: "none", Family: "dual", DNS: resolver, Outer: networkconfig.Direct{Family: "ipv4", DNS: resolver}, ConnectTimeoutSeconds: 10}
	raw, _ := json.Marshal(cfg)
	p := domain.EgressProfile{ServerID: sid, Name: "fixture", Kind: "socks5", Enabled: true}
	if err := c.api.Store.CreateEgressProfile(ctx, &p, raw); err != nil {
		t.Fatal(err)
	}
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: p.ID, EgressRevision: 1}
	if _, err := c.api.Store.SetNodeNetwork(ctx, nid, 0, policy, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.api.Desired.Publish(ctx, sid); err != nil {
		t.Fatal(err)
	}
	fetch := func(binding, egress string, conditional bool, status, count int) {
		t.Helper()
		rec, err := c.api.Store.LatestDesiredState(ctx, sid)
		if err != nil {
			t.Fatal(err)
		}
		path := agentproto.PathDesired
		if conditional {
			path += fmt.Sprintf("?if_not_revision=%d", rec.Revision)
		}
		req, _ := http.NewRequest("GET", c.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+c.agent)
		req.Header.Set(agentproto.NetworkBindingHeader, binding)
		req.Header.Set(agentproto.NetworkEgressHeader, egress)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatal("unexpected transport negotiation response", resp.StatusCode, err)
		}
		if status != http.StatusOK {
			if strings.Contains(string(data), `"credentials"`) || strings.Contains(string(data), `"params"`) {
				t.Fatal("unsupported client received configuration")
			}
			return
		}
		var ds agentproto.DesiredState
		if err = json.Unmarshal(data, &ds); err != nil || len(ds.Nodes) != count || ds.NetworkEgressVersion != 1 || ds.Hash != agentproto.ContentHash(&ds) {
			t.Fatal("transport contract missing in desired response", err)
		}
	}
	fetch("1", "", true, 409, 1)
	fetch("1", "2", true, 409, 1)
	fetch("", "1", false, 409, 1)
	fetch("1", "1", false, 200, 1)
	fetch("1", "1", true, 304, 1)
	c.do("DELETE", fmt.Sprintf("/api/v1/nodes/%d", nid), nil, 204)
	if err := c.api.Store.DeleteEgressProfile(ctx, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	fetch("1", "", true, 409, 0)
	fetch("1", "1", false, 200, 0)
}
