package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestEgressAPIVersionsDependenciesAndRuntimeReceipts(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	sid := int64(c.do("POST", "/api/v1/servers", map[string]any{"name": "egress fixture"}, 201)["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	listPath := base + "/egress-profiles"
	direct := networkconfig.Direct{InterfaceID: strings.Repeat("1", 32), Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}
	body := map[string]any{"operation_id": strings.Repeat("a", 32), "expected_revision": 0, "name": "direct", "kind": "direct", "enabled": true, "config": direct}
	reviewEgressBody(c, listPath, "create", body)
	created := c.do("POST", listPath, body, 202)
	pid := int64(created["resource_id"].(float64))
	path := fmt.Sprintf("/api/v1/egress-profiles/%d", pid)
	if created["status"] != "saved" || created["generation"] != float64(0) {
		t.Fatal("template falsely claimed runtime publication", created)
	}
	if replay := c.do("POST", listPath, body, 202); replay["id"] != created["id"] {
		t.Fatal("create retry lost identity")
	}
	if list := c.do("GET", listPath, nil, 200)["list"].([]any); len(list) != 1 {
		t.Fatal("create replay duplicated profile")
	}
	// Changed content with the same key is never a new mutation.
	body["name"] = "changed"
	c.do("POST", listPath, body, 409)
	body["name"] = "direct"
	et := c.do("POST", base+"/enroll-token", nil, 200)
	en := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: et["token"].(string)}, 200)
	c.agent = en["agent_token"].(string)
	nid := int64(c.do("POST", base+"/nodes", map[string]any{"protocol": "ss", "port": 21001}, 201)["id"].(float64))
	npath := fmt.Sprintf("/api/v1/nodes/%d/network", nid)
	hb := agentproto.Heartbeat{TS: time.Now().UTC(), Epoch: "boot", Diagnostics: agentproto.Diagnostics{NetworkBindingVersion: 1, NetworkConfigureAllowed: true, SecurityVersion: 1, SecurityPolicy: true, Nftables: true, Systemd: true,
		Cores: []agentproto.CoreStatus{{Name: "sing-box", Version: domain.DefaultSingBoxVersion, Installed: true}}}, Metrics: agentproto.Metrics{Arch: "arm64", Network: &agentproto.NetworkSnapshot{
		Version: 1, CollectorID: strings.Repeat("b", 32), BootID: "boot", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: direct.InterfaceID, Generation: strings.Repeat("c", 32), Name: "wan0", Index: 2, Kind: "physical", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}}}
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: pid, EgressRevision: 1}
	binding := map[string]any{"operation_id": strings.Repeat("d", 32), "expected_revision": 0, "network": policy}
	reviewNodeBody(c, npath, binding)
	c.do("PUT", npath, binding, 202)
	// New defaults do not change already pinned consumers or publish a runtime edit.
	direct.DNS.Address = "192.0.2.54"
	body["operation_id"], body["expected_revision"], body["config"] = strings.Repeat("e", 32), 1, direct
	reviewEgressBody(c, path, "update", body)
	if updated := c.do("PUT", path, body, 202); updated["status"] != "saved" || updated["resource_revision"] != float64(2) {
		t.Fatal(updated)
	}
	view := c.do("GET", path+"?limit=1", nil, 200)
	refs := view["references"].([]any)
	if view["reference_count"] != float64(1) || len(refs) != 1 || refs[0].(map[string]any)["revision"] != float64(1) {
		t.Fatal("reference does not show pinned version", view)
	}
	if len(c.do("GET", path+"?offset=1", nil, 200)["references"].([]any)) != 0 {
		t.Fatal("references ignored pagination")
	}
	v1 := c.do("GET", path+"/revisions/1", nil, 200)
	if v1["config"].(map[string]any)["dns"].(map[string]any)["address"] != "192.0.2.53" {
		t.Fatal("immutable revision overwritten")
	}
	c.do("GET", path+"/revisions/99", nil, 404)
	del := map[string]any{"operation_id": strings.Repeat("f", 32), "expected_revision": 2}
	reviewEgressBody(c, path, "delete", del)
	blocked := c.do("DELETE", path, del, 409)
	if blocked["error"].(map[string]any)["code"] != "egress_in_use" || blocked["reference_count"] != float64(1) {
		t.Fatal("delete did not explain dependencies", blocked)
	}
	// Offline revocation saves intent; publication and exact receipt are separate.
	if _, err := c.api.Store.DB().Exec(`UPDATE agents SET last_seen_at=NULL WHERE server_id=?`, sid); err != nil {
		t.Fatal(err)
	}
	body["operation_id"], body["expected_revision"], body["enabled"] = strings.Repeat("2", 32), 2, false
	reviewEgressBody(c, path, "update", body)
	stopped := c.do("PUT", path, body, 202)
	if stopped["status"] != "queued" {
		t.Fatal("offline disable claimed completion", stopped)
	}
	if err := c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	op, err := c.api.Store.NetworkOperation(ctx, stopped["id"].(string))
	if err != nil || op.Status != "waiting_agent" {
		t.Fatal(op, err)
	}
	rec, err := c.api.Store.LatestDesiredState(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	var ds agentproto.DesiredState
	if err := json.Unmarshal(rec.Payload, &ds); err != nil || len(ds.Nodes) != 1 || !ds.Nodes[0].Blocked || ds.Nodes[0].Network.Direct.DNS.Address != "192.0.2.53" {
		t.Fatal("disabled publication lost block or pinned resolver", err)
	}
	opPath := "/api/v1/network/operations/" + op.ID
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: op.DesiredRevision, Hash: strings.Repeat("f", 64), Status: "applied"}, 204)
	if c.do("GET", opPath, nil, 200)["status"] != "waiting_agent" {
		t.Fatal("unrelated receipt completed revocation")
	}
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: op.DesiredRevision, Hash: op.DesiredHash, Status: "applied"}, 204)
	if c.do("GET", opPath, nil, 200)["status"] != "applied" {
		t.Fatal("exact receipt not recorded")
	}
	// Even a valid new default cannot re-enable an invalid pinned old version.
	direct.InterfaceID = strings.Repeat("3", 32)
	body["operation_id"], body["expected_revision"], body["enabled"], body["config"] = strings.Repeat("4", 32), 3, true, direct
	hb.Metrics.Network.Sequence++
	hb.Metrics.Network.Interfaces[0].ID = direct.InterfaceID
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	reviewEgressBody(c, path, "update", body)
	c.do("PUT", path, body, 409)
	view = c.do("GET", path, nil, 200)
	if p := view["profile"].(map[string]any); p["enabled"] != false || p["current_revision"] != float64(3) {
		t.Fatal("failed enable partially saved", p)
	}
	hb.Metrics.Network.Sequence++
	hb.Metrics.Network.Interfaces[0].ID = strings.Repeat("1", 32)
	c.do("POST", "/api/agent/v1/heartbeat", hb, 200)
	if resumed := c.do("PUT", path, body, 202); resumed["status"] != "queued" {
		t.Fatal(resumed)
	}
	reset := map[string]any{"operation_id": strings.Repeat("5", 32), "expected_revision": 1, "network": nil}
	reviewNodeBody(c, npath, reset)
	c.do("PUT", npath, reset, 202)
	del["expected_revision"] = 4
	reviewEgressBody(c, path, "delete", del)
	if deleted := c.do("DELETE", path, del, 202); deleted["status"] != "saved" {
		t.Fatal(deleted)
	}
	c.do("GET", path, nil, 404)
	c.do("DELETE", path, del, 202) // replay remains available after resource deletion
	audits, err := c.api.Store.ListAudit(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, a := range audits {
		if strings.HasPrefix(a.Action, "egress.") {
			count++
			if a.Username != "admin" || a.UserID == nil || strings.Contains(string(a.Detail), "192.0.2.") {
				t.Fatal("audit lost actor or copied config")
			}
		}
	}
	if count != 5 {
		t.Fatal("rejected/replayed operations changed audit count", count)
	}
}

func TestEgressAPIStrictInputAuthorizationAndCSRF(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	sid := int64(c.do("POST", "/api/v1/servers", map[string]any{"name": "input fixture"}, 201)["id"].(float64))
	listPath := fmt.Sprintf("/api/v1/servers/%d/egress-profiles", sid)
	valid := `{"operation_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_revision":0,"name":"direct","kind":"direct","enabled":true,"config":{"interface_id":"11111111111111111111111111111111","family":"ipv4","dns":{"transport":"udp","address":"192.0.2.53","port":53}}}`
	for _, bad := range []string{
		`null`, `{}`, strings.Replace(valid, `"enabled":true,`, ``, 1), strings.Replace(valid, `"expected_revision":0,`, ``, 1),
		strings.Replace(valid, `"enabled":true`, `"enabled":null`, 1), strings.Replace(valid, `"expected_revision":0`, `"expected_revision":1`, 1),
		strings.Replace(valid, `"name":"direct"`, `"name":"direct","server_id":999`, 1),
		strings.Replace(valid, `"name":"direct"`, `"name":"direct","name":"other"`, 1),
		strings.Replace(valid, `"family":"ipv4"`, `"family":"ipv4","command":"untrusted"`, 1),
		strings.Replace(valid, `"kind":"direct"`, `"kind":"ssh"`, 1),
	} {
		c.do("POST", listPath, json.RawMessage(bad), 400)
	}
	c.do("POST", listPath, json.RawMessage(valid), 428)
	var validBody map[string]any
	if err := json.Unmarshal([]byte(valid), &validBody); err != nil {
		t.Fatal(err)
	}
	reviewEgressBody(c, listPath, "create", validBody)
	created := c.do("POST", listPath, validBody, 202)
	path := fmt.Sprintf("/api/v1/egress-profiles/%.0f", created["resource_id"])
	c.do("PUT", path, json.RawMessage(valid), 400)
	c.do("DELETE", path, map[string]any{"operation_id": strings.Repeat("b", 32), "expected_revision": 1, "force": true}, 400)
	// Route ownership is part of the fingerprint, even if that server is absent.
	c.do("POST", "/api/v1/servers/99999/egress-profiles", json.RawMessage(valid), 409)
	c.do("POST", "/api/v1/servers/99999/egress-profiles", json.RawMessage(strings.Replace(valid, strings.Repeat("a", 32), strings.Repeat("c", 32), 1)), 404)
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		target := path
		if method == "POST" {
			target = listPath
		}
		req, _ := http.NewRequest(method, c.srv.URL+target, strings.NewReader(valid))
		req.AddCookie(c.cookie)
		req.Header.Set("Origin", c.srv.URL)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatal("write bypassed CSRF", method, res.StatusCode)
		}
	}
	c.do("POST", "/api/v1/users", map[string]any{"username": "viewer", "password": "password123", "role": "user", "enabled": true}, 201)
	c.cookie = nil
	for _, want := range []int{401, 403} {
		for _, endpoint := range []struct{ method, path string }{{"GET", listPath}, {"POST", listPath}, {"POST", listPath + "/preview"}, {"POST", path + "/preview"}, {"GET", path}, {"PUT", path}, {"DELETE", path}, {"GET", path + "/revisions/1"}} {
			c.do(endpoint.method, endpoint.path, nil, want)
		}
		if want == 401 {
			c.do("POST", "/api/v1/auth/login", map[string]any{"username": "viewer", "password": "password123"}, 200)
		}
	}
}

func TestSOCKS5ManagementCredentialsAndReview(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "password123"}, 200)
	sid := int64(c.do("POST", "/api/v1/servers", map[string]any{"name": "SOCKS API fixture"}, 201)["id"].(float64))
	path := fmt.Sprintf("/api/v1/servers/%d/egress-profiles", sid)
	cfg := networkconfig.SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "password", Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}, Outer: networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.54", Port: 53}}, ConnectTimeoutSeconds: 10}
	first := networkconfig.SOCKS5Credentials{Username: " fixture-user-one ", Password: " fixture-password-one "}
	second := networkconfig.SOCKS5Credentials{Username: "fixture-user-two", Password: "fixture-password-two"}
	assertPublic := func(v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{first.Username, first.Password, second.Username, second.Password} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("public response or audit returned upstream credentials")
			}
		}
	}
	body := map[string]any{"operation_id": strings.Repeat("1", 32), "expected_revision": 0, "name": "SOCKS fixture", "kind": "socks5", "enabled": true, "config": cfg}
	c.do("POST", path, body, 400) // password mode requires an explicit pair
	body["credentials"] = map[string]any{"username": first.Username, "password": first.Password, "unknown": true}
	c.do("POST", path, body, 400)
	body["credentials"] = first
	assertPublic(reviewEgressBody(c, path, "create", body))
	body["credentials"] = second
	c.do("POST", path, body, 409) // the review also commits to the exact new credentials
	body["credentials"] = first
	created := c.do("POST", path, body, 202)
	assertPublic(created)
	c.do("POST", path, body, 202)
	body["credentials"] = second
	c.do("POST", path, body, 409) // same operation cannot rotate credentials on replay
	pid := int64(created["resource_id"].(float64))
	path = fmt.Sprintf("/api/v1/egress-profiles/%d", pid)
	view := c.do("GET", path, nil, 200)
	assertPublic(view)
	if view["revision"].(map[string]any)["has_credentials"] != true {
		t.Fatal("editor cannot retain current credentials")
	}
	nid := int64(c.do("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", sid), map[string]any{"protocol": "ss", "port": 21002}, 201)["id"].(float64))
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: pid, EgressRevision: 1}
	if _, err := c.api.Store.SetNodeNetwork(ctx, nid, 0, policy, nil); err != nil {
		t.Fatal(err)
	}
	assertBound := func(want networkconfig.SOCKS5Credentials) {
		t.Helper()
		bound, err := c.api.Store.DeployedNodeNetworks(ctx, sid)
		if err != nil || len(bound) != 1 || bound[0].Credentials == nil || *bound[0].Credentials != want {
			t.Fatal("fixed version lost its exact credential pair", err)
		}
	}
	assertBound(first)
	// Omission creates an immutable version carrying the current credentials.
	delete(body, "credentials")
	body["operation_id"], body["expected_revision"] = strings.Repeat("2", 32), 1
	assertPublic(reviewEgressBody(c, path, "update", body))
	assertPublic(c.do("PUT", path, body, 202))
	policy.EgressRevision = 2
	if _, err := c.api.Store.SetNodeNetwork(ctx, nid, 1, policy, nil); err != nil {
		t.Fatal(err)
	}
	assertBound(first)
	body["operation_id"], body["expected_revision"], body["credentials"] = strings.Repeat("3", 32), 2, second
	assertPublic(reviewEgressBody(c, path, "update", body))
	assertPublic(c.do("PUT", path, body, 202))
	assertBound(first) // saving a rotation does not migrate consumers
	policy.EgressRevision = 3
	if _, err := c.api.Store.SetNodeNetwork(ctx, nid, 2, policy, nil); err != nil {
		t.Fatal(err)
	}
	assertBound(second)
	delete(body, "credentials")
	cfg.Authentication = "none"
	body["operation_id"], body["expected_revision"], body["config"] = strings.Repeat("4", 32), 3, cfg
	assertPublic(reviewEgressBody(c, path, "update", body))
	assertPublic(c.do("PUT", path, body, 202))
	view = c.do("GET", path, nil, 200)
	if view["revision"].(map[string]any)["has_credentials"] == true {
		t.Fatal("no-auth latest version claims credentials")
	}
	assertBound(second)
	for version := 1; version <= 4; version++ {
		assertPublic(c.do("GET", fmt.Sprintf("%s/revisions/%d", path, version), nil, 200))
	}
	cfg.Authentication = "password"
	body["operation_id"], body["expected_revision"], body["config"] = strings.Repeat("5", 32), 4, cfg
	previewBody := map[string]any{"action": "update", "expected_revision": 4, "name": "SOCKS fixture", "kind": "socks5", "enabled": true, "config": cfg}
	c.do("POST", path+"/preview", previewBody, 400) // cannot revive older credentials
	assertPublic(c.do("GET", path, nil, 200))
	audits, err := c.api.Store.ListAudit(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertPublic(audits)
}
