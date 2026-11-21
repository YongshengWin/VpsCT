package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

func TestPortChangeBearerAcknowledgementAndRetry(t *testing.T) {
	c := setupAPIKeyTest(t)
	ctx := context.Background()
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	read, _ := issueTestAPIKey(t, c, 1, "read")
	srv, _ := keyRequest(t, c, "POST", "/api/v1/servers", key, map[string]any{"name": "port-fixture", "public_host": "1.1.1.1"}, nil, 201)
	sid := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	enrollment, _ := keyRequest(t, c, "POST", base+"/enroll-token", key, nil, nil, 200)
	enrolled := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: enrollment["token"].(string)}, 200)
	c.agent = enrolled["agent_token"].(string)
	node, _ := keyRequest(t, c, "POST", base+"/nodes", key, map[string]any{"protocol": "ss", "port": 21001}, nil, 201)
	nid := int64(node["id"].(float64))
	path := fmt.Sprintf("/api/v1/nodes/%d/network", nid)
	hb := agentproto.Heartbeat{TS: time.Now().UTC(), Epoch: "port-fixture-boot", Diagnostics: agentproto.Diagnostics{NetworkBindingVersion: agentproto.NetworkBindingVersion, NetworkConfigureAllowed: true, SecurityVersion: 1, SecurityPolicy: true, Nftables: true, Systemd: true, Cores: []agentproto.CoreStatus{{Name: "sing-box", Version: domain.DefaultSingBoxVersion, Installed: true}}}, Metrics: agentproto.Metrics{Arch: "amd64", Network: &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("b", 32), BootID: "port-fixture-boot", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: strings.Repeat("1", 32), Generation: strings.Repeat("c", 32), Name: "wan0", Index: 2, Kind: "physical", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}}}
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	body := map[string]any{"operation_id": strings.Repeat("d", 32), "expected_revision": 0, "network": nil, "listen_port": 22001}
	keyRequest(t, c, "PUT", path, read, body, nil, 403)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "port-member", "password": "test-fixture-password", "role": "user"}, 201)
	mk, _ := issueTestMemberSession(t, c, int64(member["id"].(float64)), "read", "write")
	keyRequest(t, c, "PUT", path, mk, body, nil, 403)
	keyRequest(t, c, "PUT", base+"/unavailable-ports/22001", key, map[string]any{"transport": "tcp", "network": "different test network", "reason": "manual observation"}, nil, 200)
	preview, _ := keyRequest(t, c, "POST", path+"/preview", key, map[string]any{"network": nil, "listen_port": 22001}, nil, 200)
	if warning, ok := preview["unavailable_port"].(map[string]any); !ok || warning["port"] != float64(22001) {
		t.Fatal("port warning missing from Bearer preview", preview)
	}
	if preview["ready"] != true {
		t.Fatal(preview)
	}
	body["expected_impact"] = preview["impact"].(map[string]any)["token"]
	op, _ := keyRequest(t, c, "PUT", path, key, body, nil, 202)
	if op["status"] != "queued" {
		t.Fatal(op)
	}
	if err := c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err := c.api.Store.NetworkOperation(ctx, body["operation_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: operation.DesiredRevision, Hash: operation.DesiredHash, Status: "failed", Error: "fixture"}, 204)
	retryPath := "/api/v1/network/operations/" + operation.ID + "/retry"
	keyRequest(t, c, "POST", retryPath, key, map[string]any{"expected_retry_revision": operation.RetryRevision}, nil, 202)
	if err = c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	operation, _ = c.api.Store.NetworkOperation(ctx, operation.ID)
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: operation.DesiredRevision, Hash: operation.DesiredHash, Status: "applied"}, 204)
	final, _ := keyRequest(t, c, "GET", "/api/v1/network/operations/"+operation.ID, key, nil, nil, 200)
	if final["status"] != "applied" {
		t.Fatal(final)
	}
	saved, _ := c.api.Store.GetNode(ctx, nid)
	if saved.Port != 22001 || saved.ListenPort != 22001 {
		t.Fatal("port not saved")
	}
	if _, usable, err := c.api.Store.AppliedSubscriptionNode(ctx, saved); err != nil || !usable {
		t.Fatal("acknowledged port fenced", err)
	}
	replay, _ := keyRequest(t, c, "PUT", path, key, body, nil, 202)
	if replay["id"] != op["id"] {
		t.Fatal("retry changed identity")
	}
}
