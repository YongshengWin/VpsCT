package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/maintenance"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func TestNetworkManagementAdmissionPreviewSaveResetAndMaintenance(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	srv := c.do("POST", "/api/v1/servers", map[string]any{"name": "readiness fixture"}, 201)
	sid := int64(srv["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	et := c.do("POST", base+"/enroll-token", nil, 200)
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string)}, 200)
	c.agent = en["agent_token"].(string)
	node := c.do("POST", base+"/nodes", map[string]any{"protocol": "ss", "port": 21001}, 201)
	nid := int64(node["id"].(float64))
	path := fmt.Sprintf("/api/v1/nodes/%d/network", nid)
	capabilities := base + "/network/capabilities"
	if c.do("GET", capabilities, nil, 200)["ready"] != false {
		t.Fatal("unknown diagnostics were eligible")
	}
	policy := networkconfig.Node{ListenMode: "address", ListenAddress: "192.0.2.1", ListenInterfaceID: strings.Repeat("1", 32), AdvertiseMode: "inherit", OnUnavailable: "block"}
	in := map[string]any{"operation_id": strings.Repeat("a", 32), "expected_revision": 0, "network": policy}
	c.do("POST", path+"/preview", map[string]any{}, 400)
	c.do("PUT", path, in, 428)
	reviewNodeBody(c, path, in)
	c.do("PUT", path, in, 409)
	hb := agentproto.Heartbeat{TS: time.Now().UTC(), Epoch: "boot", Diagnostics: agentproto.Diagnostics{NetworkBindingVersion: 1, NetworkConfigureAllowed: true, SecurityVersion: 1, SecurityPolicy: true, Nftables: true, Systemd: true,
		Cores: []agentproto.CoreStatus{{Name: "sing-box", Version: domain.DefaultSingBoxVersion, Installed: true}}}, Metrics: agentproto.Metrics{Arch: "arm64", Network: &agentproto.NetworkSnapshot{
		Version: 1, CollectorID: strings.Repeat("b", 32), BootID: "boot", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: policy.ListenInterfaceID, Generation: strings.Repeat("c", 32), Name: "wan0", Index: 2, Kind: "physical", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}}}
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	if c.do("GET", capabilities, nil, 200)["ready"] != true {
		t.Fatal("supported current diagnostics were rejected")
	}
	if reviewNodeBody(c, path, in)["ready"] != true {
		t.Fatal("valid selection preview rejected")
	}
	c.do("POST", path+"/preview", map[string]any{"network": policy, "advertise_host": "fixture.example"}, 400)
	independent := policy
	independent.AdvertiseMode = "override"
	c.do("POST", path+"/preview", map[string]any{"network": independent, "advertise_host": "https://fixture.example/path"}, 400)
	c.do("POST", path+"/preview", map[string]any{"network": independent, "advertise_host": "fixture.example"}, 200)
	malformed := map[string]any{"operation_id": strings.Repeat("a", 32), "expected_revision": 0}
	c.do("PUT", path, malformed, 400) // omission must not mean reset
	malformed["network"] = map[string]any{"listen_mode": "all", "advertise_mode": "inherit", "on_unavailable": "block", "runtime_network": map[string]any{"ifindex": 2}}
	c.do("PUT", path, malformed, 400)
	op := c.do("PUT", path, in, 202)
	if op["status"] != "queued" || op["resource_revision"] != float64(1) {
		t.Fatal("save claimed application or lost edit version", op)
	}
	in["operation_id"] = strings.Repeat("d", 32)
	c.do("PUT", path, in, 409) // stale editor
	in["operation_id"] = strings.Repeat("a", 32)
	job := store.MaintenanceJob{ServerID: sid, Job: maintenance.Job{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "update", Version: "v0.1.0"}, Status: "queued", CreatedAt: c.api.Store.Now(), UpdatedAt: c.api.Store.Now()}}
	if err := c.api.Store.CreateMaintenance(ctx, job); err != nil {
		t.Fatal(err)
	}
	if c.do("GET", capabilities, nil, 200)["ready"] != false {
		t.Fatal("maintenance reservation absent from admission")
	}
	c.do("PUT", path, in, 202) // exact accepted retry remains readable
	c.do("POST", base+"/republish", map[string]any{}, 409)
	if err := c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal("parked queue reported a publication failure", err)
	}
	reset := map[string]any{"operation_id": strings.Repeat("e", 32), "expected_revision": 1, "network": nil}
	reviewNodeBody(c, path, reset)
	c.do("PUT", path, reset, 409)
	req, _ := http.NewRequest("GET", c.srv.URL+"/api/agent/v1/desired", nil)
	req.Header.Set("Authorization", "Bearer "+c.agent)
	req.Header.Set(agentproto.NetworkBindingHeader, "1")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatal("desired fetch bypassed active maintenance", response.StatusCode)
	}
	job.Status = "failed"
	if err = c.api.Store.SaveMaintenance(ctx, job, "queued"); err != nil {
		t.Fatal(err)
	}
	if err = c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	// Loss of the old NIC rejects new binding, but explicit reset remains usable.
	hb.Metrics.Network.Sequence++
	hb.Metrics.Network.Interfaces[0].ID = strings.Repeat("2", 32)
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	if c.do("POST", path+"/preview", map[string]any{"network": policy}, 200)["ready"] != false {
		t.Fatal("replacement interface silently inherited binding")
	}
	c.do("PUT", path, reset, 202)
	n, err := c.api.Store.GetNode(ctx, nid)
	if err != nil || n.Network != nil || n.NetworkRevision != 2 {
		t.Fatal("explicit reset did not retain edit history", err)
	}
	c.do("PUT", path, in, 202) // cannot restore the superseded original policy
	n, _ = c.api.Store.GetNode(ctx, nid)
	if n.Network != nil || n.NetworkRevision != 2 {
		t.Fatal("old accepted retry resurrected binding")
	}
	c.do("POST", "/api/v1/users", map[string]any{"username": "viewer", "password": "password123", "role": "user", "enabled": true}, 201)
	c.cookie = nil
	c.do("GET", capabilities, nil, 401)
	c.do("PUT", path, reset, 401)
	c.do("POST", "/api/v1/auth/login", map[string]any{"username": "viewer", "password": "password123"}, 200)
	c.do("GET", capabilities, nil, 403)
	c.do("POST", path+"/preview", map[string]any{"network": policy}, 403)
	c.do("PUT", path, reset, 403)
}
