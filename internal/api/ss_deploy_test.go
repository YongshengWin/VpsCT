package api

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
	"fmt"
	"testing"
)

func TestDeploySSTCPOnlyNAT(t *testing.T) {
	c, id := natAPIClient(t)
	cfg := &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001, Network: "tcp"}, {PublicPort: 30002, LocalPort: 10002, Network: "tcp"}}}
	natAPIReport(t, c, &agentproto.NATStatus{Version: 2, Source: "local", Configured: true, Status: "applied", Config: cfg}, 200)
	endpoint := fmt.Sprintf("/api/v1/servers/%d/nodes", id)
	n := c.do("POST", endpoint, map[string]any{"protocol": "ss", "ss_method": "aes-128-gcm", "ss_network": "tcp"}, 201)
	if n["port"] != float64(30001) && n["port"] != float64(30002) {
		t.Fatal("outside NAT pool")
	}
	params := n["params"].(map[string]any)
	if params["cipher"] != "aes-128-gcm" || params["udp"] != false {
		t.Fatal("client options lost")
	}
	c.do("POST", endpoint, map[string]any{"protocol": "ss", "ss_network": "both"}, 400)
	c.do("POST", endpoint, map[string]any{"protocol": "ss"}, 400)
	c.do("POST", endpoint, map[string]any{"protocol": "ss", "ss_network": "tcp", "ss_method": "none"}, 400)
}
