package api

import (
	"ctlvps/internal/agentproto"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNetworkHeartbeatCompatibilityAndAdminScope(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "network-test"}, 201)
	sid := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	et := c.do("POST", base+"/enroll-token", nil, 200)
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string)}, 200)
	c.agent = en["agent_token"].(string)
	hb := agentproto.Heartbeat{Epoch: "boot", TS: time.Now(), Metrics: agentproto.Metrics{Interface: "eth0", NetRx: 100, NetTx: 200}}
	resp := c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	if resp["network_version"] != float64(1) {
		t.Fatal("missing negotiation")
	}
	if c.do("GET", base+"/network", nil, 200)["snapshot"] != nil {
		t.Fatal("legacy agent should have no network snapshot")
	}
	hb.Metrics.Network = &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "boot", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: strings.Repeat("b", 32), Generation: strings.Repeat("c", 32), Name: "eth0", Index: 2, Kind: "physical", CountersValid: true, Rx: 100, Tx: 200}}}
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	hb.Metrics.Network.Sequence = 2
	hb.Metrics.Network.Interfaces[0].Rx = 150
	hb.Metrics.Network.Interfaces[0].Tx = 230
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	v := c.do("GET", base+"/network", nil, 200)
	items := v["interfaces"].([]any)
	if len(items) != 1 {
		t.Fatal("missing interface")
	}
	id := int64(items[0].(map[string]any)["id"].(float64))
	path := fmt.Sprintf("%s/interfaces/%d/traffic", base, id)
	series := c.do("GET", path, nil, 200)
	if series["total_up"] != float64(50) || series["total_down"] != float64(30) {
		t.Fatal(series)
	}
	legacy := c.do("GET", base+"/traffic", nil, 200)
	if legacy["total_up"] != float64(0) || legacy["total_down"] != float64(0) {
		t.Fatal("network observations changed server quota")
	}
	hb.Metrics.Network.Sequence = 3
	hb.Metrics.Network.Interfaces[0].Rx = -1
	c.do("POST", "/api/agent/v1/heartbeat", hb, 400)
	v = c.do("GET", base+"/network", nil, 200)
	if v["snapshot"].(map[string]any)["sequence"] != float64(2) {
		t.Fatal("invalid snapshot was persisted")
	}
	// A failed observation write must not interrupt legacy traffic accounting.
	if _, err := c.api.Store.DB().Exec(`CREATE TRIGGER fail_network_api BEFORE INSERT ON network_interfaces BEGIN SELECT RAISE(ABORT,'observation unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	hb.Metrics.Network.Interfaces[0].Rx = 200
	hb.Metrics.NetRx, hb.Metrics.NetTx = 150, 230
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	v = c.do("GET", base+"/network", nil, 200)
	if v["snapshot"].(map[string]any)["status"] != "error" {
		t.Fatal("observation failure is invisible")
	}
	legacy = c.do("GET", base+"/traffic", nil, 200)
	if legacy["total_up"] != float64(50) || legacy["total_down"] != float64(30) {
		t.Fatal("observation failure interrupted quota")
	}
	c.do("GET", fmt.Sprintf("/api/v1/servers/%d/interfaces/%d/traffic", sid+1, id), nil, 404)
	admin := c.cookie
	c.cookie = nil
	c.do("GET", base+"/network", nil, 401)
	c.cookie = admin
	c.do("POST", "/api/v1/users", map[string]any{"username": "network-viewer", "password": "password123", "role": "user", "enabled": true}, 201)
	c.cookie = nil
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "network-viewer", "password": "password123"}, 200)
	c.do("GET", base+"/network", nil, 403)
	c.do("GET", path, nil, 403)
}
