package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func reviewAgentSS2022(t *testing.T, c *client, key, path, action string, body map[string]any) map[string]any {
	t.Helper()
	request := map[string]any{"action": action}
	for _, field := range []string{"expected_revision", "name", "kind", "enabled", "config", "credentials"} {
		if value, ok := body[field]; ok {
			request[field] = value
		}
	}
	view, _ := keyRequest(t, c, "POST", path+"/preview", key, request, nil, 200)
	body["expected_impact"] = view["impact"].(map[string]any)["token"]
	return view
}

func TestAgentSS2022EgressPreviewCreateUpdateAndReceipts(t *testing.T) {
	c := setupAPIKeyTest(t)
	ctx := context.Background()
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	server, _ := keyRequest(t, c, "POST", "/api/v1/servers", key, map[string]any{"name": "SS-2022 agent fixture"}, nil, 201)
	sid := int64(server["id"].(float64))
	base := fmt.Sprintf("/api/v1/servers/%d", sid)
	createPath := base + "/egress-profiles"
	config := networkconfig.SS2022{Server: "198.51.100.2", ServerPort: 8388, Method: "2022-blake3-aes-128-gcm", Family: "ipv4",
		DNS:   networkconfig.Resolver{Transport: "udp", Address: "203.0.113.53", Port: 53},
		Outer: networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.53", Port: 53}}, ConnectTimeoutSeconds: 10}
	firstSecret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 16))
	secondSecret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 16))
	assertPublic := func(value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(firstSecret)) || bytes.Contains(raw, []byte(secondSecret)) || bytes.Contains(raw, []byte(key)) {
			t.Fatal("egress metadata or audit disclosed credentials")
		}
	}
	body := map[string]any{"operation_id": strings.Repeat("1", 32), "expected_revision": 0, "name": "SS-2022 upstream", "kind": "ss2022", "enabled": true, "config": config}
	previewBody := map[string]any{"action": "create", "expected_revision": 0, "name": body["name"], "kind": "ss2022", "enabled": true, "config": config}
	keyRequest(t, c, "POST", createPath+"/preview", key, previewBody, nil, 400) // missing key
	previewBody["credentials"] = networkconfig.SOCKS5Credentials{Password: "invalid-fixture"}
	keyRequest(t, c, "POST", createPath+"/preview", key, previewBody, nil, 400) // canonical Base64 and key length remain mandatory
	body["credentials"] = networkconfig.SOCKS5Credentials{Password: firstSecret}
	keyRequest(t, c, "POST", createPath, key, body, nil, 428) // preview still required
	preview := reviewAgentSS2022(t, c, key, createPath, "create", body)
	assertPublic(preview)
	if preview["ready"] != true {
		t.Fatal("valid SS-2022 template was not eligible")
	}
	body["credentials"] = networkconfig.SOCKS5Credentials{Password: secondSecret}
	keyRequest(t, c, "POST", createPath, key, body, nil, 409) // review binds the exact secret
	body["credentials"] = networkconfig.SOCKS5Credentials{Password: firstSecret}
	created, _ := keyRequest(t, c, "POST", createPath, key, body, nil, 202)
	assertPublic(created)
	if created["status"] != "saved" || created["generation"] != float64(0) {
		t.Fatal("unused template claimed runtime application")
	}
	pid := int64(created["resource_id"].(float64))
	path := fmt.Sprintf("/api/v1/egress-profiles/%d", pid)
	view, _ := keyRequest(t, c, "GET", path, key, nil, nil, 200)
	assertPublic(view)
	if view["profile"].(map[string]any)["kind"] != "ss2022" || view["revision"].(map[string]any)["has_credentials"] != true {
		t.Fatal("SS-2022 metadata lost type or credential-presence state")
	}
	var sealed string
	if err := c.api.Store.DB().QueryRow(`SELECT credentials FROM egress_profile_credentials WHERE profile_id=? AND revision=1`, pid).Scan(&sealed); err != nil || !strings.HasPrefix(sealed, "enc:v1:") || strings.Contains(sealed, firstSecret) {
		t.Fatal("SS-2022 secret was not stored encrypted", err)
	}
	// Omitting credentials retains the encrypted key in a new immutable version.
	delete(body, "credentials")
	body["operation_id"], body["expected_revision"] = strings.Repeat("2", 32), 1
	assertPublic(reviewAgentSS2022(t, c, key, path, "update", body))
	updated, _ := keyRequest(t, c, "PUT", path, key, body, nil, 202)
	assertPublic(updated)
	if updated["status"] != "saved" || updated["resource_revision"] != float64(2) {
		t.Fatal("template update did not retain its separate save receipt")
	}
	poll, _ := keyRequest(t, c, "GET", "/api/v1/network/operations/"+updated["id"].(string), key, nil, nil, 200)
	assertPublic(poll)
	if poll["status"] != "saved" {
		t.Fatal("saved template receipt was not queryable")
	}
	body["operation_id"] = strings.Repeat("3", 32)
	keyRequest(t, c, "PUT", path, key, body, nil, 409) // stale expected_revision cannot append

	// A local machine-agent fixture proves that template access does not bypass
	// official-core admission or turn a pending bind into a completed operation.
	enrollment, _ := keyRequest(t, c, "POST", base+"/enroll-token", key, nil, nil, 200)
	enrolled := c.do("POST", "/api/agent/v1/enroll", agentproto.EnrollRequest{EnrollToken: enrollment["token"].(string)}, 200)
	c.agent = enrolled["agent_token"].(string)
	node, _ := keyRequest(t, c, "POST", base+"/nodes", key, map[string]any{"protocol": "ss", "port": 21501}, nil, 201)
	nid := int64(node["id"].(float64))
	networkPath := fmt.Sprintf("/api/v1/nodes/%d/network", nid)
	heartbeat := agentproto.Heartbeat{TS: time.Now().UTC(), Epoch: "ss2022-fixture-boot",
		Diagnostics: agentproto.Diagnostics{NetworkBindingVersion: agentproto.NetworkBindingVersion, NetworkEgressVersion: agentproto.NetworkEgressVersion,
			NetworkTransportVersion: agentproto.NetworkTransportVersion, NetworkConfigureAllowed: true, SecurityVersion: 1, SecurityPolicy: true, Nftables: true, Systemd: true,
			Cores: []agentproto.CoreStatus{{Name: "sing-box", Version: "1.13.3", Installed: true}}},
		Metrics: agentproto.Metrics{Arch: "amd64", Network: &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "ss2022-fixture-boot",
			Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: strings.Repeat("b", 32), Generation: strings.Repeat("c", 32),
				Name: "wan0", Index: 2, Kind: "physical", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}}}
	if err := c.api.Store.SetSetting(ctx, domain.SettingSingBoxVersion, "1.13.3"); err != nil {
		t.Fatal(err)
	}
	c.do("POST", "/api/agent/v1/heartbeat", heartbeat, 200)
	policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: pid, EgressRevision: 2}
	bind := map[string]any{"operation_id": strings.Repeat("4", 32), "expected_revision": 0, "network": policy}
	bindingPreview, _ := keyRequest(t, c, "POST", networkPath+"/preview", key, map[string]any{"network": policy}, nil, 200)
	assertPublic(bindingPreview)
	if bindingPreview["ready"] != false {
		t.Fatal("SS-2022 outbound accepted an unsupported pinned core")
	}
	coreRejected := false
	for _, rawCheck := range bindingPreview["checks"].([]any) {
		check := rawCheck.(map[string]any)
		if check["code"] == "ss2022_core" && check["ready"] == false {
			coreRejected = true
		}
	}
	if !coreRejected {
		t.Fatal("unsupported core was not rejected by the SS-2022 capability check")
	}
	bind["expected_impact"] = bindingPreview["impact"].(map[string]any)["token"]
	keyRequest(t, c, "PUT", networkPath, key, bind, nil, 409)
	if pin := c.api.Store.GetSetting(ctx, domain.SettingSingBoxVersion, ""); pin != "1.13.3" {
		t.Fatal("selecting SS-2022 changed the administrator's core pin")
	}
	if err := c.api.Store.SetSetting(ctx, domain.SettingSingBoxVersion, domain.DefaultSingBoxVersion); err != nil {
		t.Fatal(err)
	}
	heartbeat.Diagnostics.Cores[0].Version = domain.DefaultSingBoxVersion
	heartbeat.Metrics.Network.Sequence++
	c.do("POST", "/api/agent/v1/heartbeat", heartbeat, 200)
	bindingPreview, _ = keyRequest(t, c, "POST", networkPath+"/preview", key, map[string]any{"network": policy}, nil, 200)
	assertPublic(bindingPreview)
	if bindingPreview["ready"] != true {
		t.Fatal("official compatible SS-2022 binding was not eligible", bindingPreview)
	}
	bind["expected_impact"] = bindingPreview["impact"].(map[string]any)["token"]
	bound, _ := keyRequest(t, c, "PUT", networkPath, key, bind, nil, 202)
	assertPublic(bound)
	opPath := "/api/v1/network/operations/" + bound["id"].(string)
	poll, _ = keyRequest(t, c, "GET", opPath, key, nil, nil, 200)
	if poll["status"] != "queued" {
		t.Fatal("binding was reported complete before publication")
	}
	if err := c.api.Desired.ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	poll, _ = keyRequest(t, c, "GET", opPath, key, nil, nil, 200)
	assertPublic(poll)
	if poll["status"] != "waiting_agent" {
		t.Fatal("published binding was not awaiting a receipt")
	}
	op, err := c.api.Store.NetworkOperation(ctx, bound["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	record, err := c.api.Store.LatestDesiredState(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	var desired agentproto.DesiredState
	if err := json.Unmarshal(record.Payload, &desired); err != nil || len(desired.Nodes) != 1 || desired.Nodes[0].Network == nil || desired.Nodes[0].Network.SS2022 == nil || desired.Nodes[0].Network.SS2022.Credentials.Password != firstSecret {
		t.Fatal("retained SS-2022 key did not reach the authenticated agent payload", err)
	}
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: op.DesiredRevision, Hash: strings.Repeat("f", 64), Status: "applied"}, 400)
	poll, _ = keyRequest(t, c, "GET", opPath, key, nil, nil, 200)
	if poll["status"] != "waiting_agent" {
		t.Fatal("unrelated receipt completed the SS-2022 binding")
	}
	c.do("POST", "/api/agent/v1/apply-report", agentproto.ApplyReport{Revision: op.DesiredRevision, Hash: op.DesiredHash, Status: "applied"}, 204)
	poll, _ = keyRequest(t, c, "GET", opPath, key, nil, nil, 200)
	assertPublic(poll)
	if poll["status"] != "applied" {
		t.Fatal("exact receipt did not complete the SS-2022 binding")
	}
	audits, err := c.api.Store.ListAudit(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertPublic(audits)
}
