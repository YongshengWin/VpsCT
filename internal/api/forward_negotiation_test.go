package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func TestForwardDesiredRequiresFreshCapabilityEvenForEmptyCleanup(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "forward-negotiation"}, 201)
	sid := int64(srv["id"].(float64))
	et := c.do("POST", fmt.Sprintf("/api/v1/servers/%d/enroll-token", sid), nil, 200)
	en := c.do("POST", agentproto.PathEnroll, agentproto.EnrollRequest{EnrollToken: et["token"].(string)}, 200)
	c.agent = en["agent_token"].(string)
	cfg := networkconfig.Forward{ListenMode: "all", ListenPort: 25101, Network: "tcp", TargetHost: "192.0.2.2", TargetPort: 443, SourceMode: "cidr", MaxTCPConnections: 32}
	op, err := c.api.Store.RequestPortForward(ctx, store.PortForwardRequest{ID: strings.Repeat("e", 32), Action: "create", ServerID: sid, Name: "forward", Enabled: true, Config: &cfg}, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.api.Desired.Publish(ctx, sid); err != nil {
		t.Fatal(err)
	}
	fetch := func(version string, conditional bool, status, count int) {
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
		req.Header.Set(agentproto.NetworkBindingHeader, "1")
		req.Header.Set(agentproto.NetworkForwardHeader, version)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != status {
			t.Fatal("wrong forward negotiation result", resp.StatusCode)
		}
		if status != 200 {
			return
		}
		var ds agentproto.DesiredState
		if err := json.NewDecoder(resp.Body).Decode(&ds); err != nil {
			t.Fatal(err)
		}
		if ds.NetworkForwardVersion != 1 || ds.NetworkBindingVersion != 1 || len(ds.Forwards) != count || len(ds.Nodes) != 0 || ds.Hash != agentproto.ContentHash(&ds) {
			t.Fatal("forward missing, mixed with nodes or unbound hash")
		}
	}
	fetch("", true, 409, 1)
	fetch("2", false, 409, 1)
	fetch("1", false, 200, 1)
	fetch("1", true, 304, 1)
	// Simulate final cleanup storage removal; old executables still cannot
	// receive an empty payload and erase an outstanding local resource fence.
	if _, err := c.api.Store.DB().Exec(`DELETE FROM port_forwards WHERE id=?`, op.ResourceID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.api.Desired.Publish(ctx, sid); err != nil {
		t.Fatal(err)
	}
	fetch("", true, 409, 0)
	fetch("1", false, 200, 0)
}
