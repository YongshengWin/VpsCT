package api

import (
	"context"
	"ctlvps/internal/agentproto"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNetworkBillingAPIRequiresCapabilityAndAcknowledgment(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "billing-fixture"}, 201)
	sid := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	e := c.do("POST", base+"/enroll-token", nil, 200)
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: e["token"].(string)}, 200)
	c.agent = en["agent_token"].(string)
	path := base + "/network/billing"
	id := strings.Repeat("a", 32)
	input := map[string]any{"expected_revision": 0, "mode": "interfaces", "interface_ids": []string{id}, "boundary_confirmed": true}
	c.do("PUT", path, input, 409)
	n := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("b", 32), BootID: "boot", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: strings.Repeat("c", 32), Name: "eth0", Index: 2, Kind: "physical", CountersValid: true, Rx: 500, Tx: 600}}}
	hb := agentproto.Heartbeat{TS: time.Now().UTC(), Epoch: "boot", Metrics: agentproto.Metrics{NetRx: 100, NetTx: 200, Network: n}, Diagnostics: agentproto.Diagnostics{NetworkBillingVersion: 1}}
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	input["boundary_confirmed"] = false
	c.do("PUT", path, input, 400)
	input["boundary_confirmed"] = true
	view := c.do("PUT", path, input, 200)
	if view["current"].(map[string]any)["revision"] != float64(0) || view["requested"].(map[string]any)["revision"] != float64(1) {
		t.Fatal("API changed live billing without settlement")
	}
	c.do("PUT", path, input, 409)
	resp := c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	if resp["network_billing_version"] != float64(1) || resp["network_billing_requested"].(map[string]any)["revision"] != float64(1) {
		t.Fatal("policy not negotiated")
	}
	sw := &agentproto.NetworkBillingSwitch{ID: strings.Repeat("d", 32), Next: agentproto.NetworkBillingPolicy{Revision: 1, Mode: "interfaces", InterfaceIDs: []string{id}}, TS: time.Now().UTC(), Legacy: agentproto.LegacyNetworkCounters{Epoch: "boot", Rx: 110, Tx: 220}, Snapshot: n}
	c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{NetworkBillingSwitch: sw, Ports: []agentproto.PortCounter{{Port: 1234}}}, 400)
	c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{NetworkBillingLegacy: &agentproto.LegacyNetworkCounters{Epoch: strings.Repeat("e", 257)}}, 400)
	// A cutover tail enforces the server quota even without a notifier.
	server, err := c.api.Store.GetServer(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	server.Enabled, server.QuotaBytes, server.QuotaResetDay = true, 20, 1
	if err = c.api.Store.UpdateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	if err = c.api.Store.SetSetting(context.Background(), "quota.action", "stop"); err != nil {
		t.Fatal(err)
	}
	resp = c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{NetworkBillingSwitch: sw}, 200)
	if resp["network_billing_ack"] != sw.ID {
		t.Fatal("missing ACK")
	}
	server, err = c.api.Store.GetServer(context.Background(), sid)
	if err != nil || server.Enabled {
		t.Fatal("cutover tail did not enforce quota", err)
	}
	c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{NetworkBillingSwitch: sw}, 200)
	view = c.do("GET", path, nil, 200)
	if view["current"].(map[string]any)["revision"] != float64(1) {
		t.Fatal("settlement not applied")
	}
	series := c.do("GET", base+"/traffic", nil, 200)
	if series["total_up"] != float64(10) || series["total_down"] != float64(20) {
		t.Fatal("old tail not accounted once", series)
	}
	c.cookie = nil
	c.do("GET", path, nil, 401)
	c.do("PUT", path, input, 401)
}
