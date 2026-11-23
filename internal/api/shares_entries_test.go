package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/nodecombo"
)

func TestShareEntryBearerLifecycleAndLegacyClientGuard(t *testing.T) {
	c := newTestAPI(t)
	c.do("POST", "/api/v1/auth/setup", map[string]any{"setup_token": testSetupToken, "username": "admin", "password": "test-password"}, 200)
	member := c.do("POST", "/api/v1/users", map[string]any{"username": "member", "password": "test-password"}, 201)
	admin, _ := issueTestAPIKey(t, c, 1, "read", "write")
	readonly, _ := issueTestAPIKey(t, c, 1, "read")
	other, _ := issueTestMemberSession(t, c, int64(member["id"].(float64)), "read", "write")
	srv := domain.Server{Name: "fixture", PublicHost: "fixture.test", Enabled: true, CoreMode: domain.CoreModeStable}
	if err := c.api.Store.CreateServer(t.Context(), &srv); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"protocol": "ss"}
	entry := map[string]any{"id": "ss-tcp", "snapshot": config, "quota_bytes": 100}
	target := map[string]any{"server_id": srv.ID, "entries": []any{entry}, "protocols": []string{"ss"}}
	body := map[string]any{"name": "fixture", "targets": []any{target}, "entry_version": 1}
	base := "/api/v1/shares"
	keyRequest(t, c, "POST", base, readonly, body, nil, 403)
	config["unmodelled"] = true
	keyRequest(t, c, "POST", base, admin, body, nil, 400)
	delete(config, "unmodelled")
	out, _ := keyRequest(t, c, "POST", base, admin, body, nil, 201)
	id := int64(out["id"].(float64))
	uri := fmt.Sprintf("%s/%d", base, id)
	keyRequest(t, c, "GET", uri, other, nil, nil, 403)
	keyRequest(t, c, "POST", uri+"/reissue", readonly, nil, nil, 403)
	get := func() domain.Share {
		t.Helper()
		s, err := c.api.Store.GetShare(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := get()
	e := s.Targets[0].Entries[0]
	if e.NodeID == 0 || e.Snapshot.SSNetwork != "tcp" || e.QuotaBytes != 100 {
		t.Fatal("missing normalized snapshot or ID")
	}
	keyRequest(t, c, "PUT", uri, admin, map[string]any{"name": "old client", "targets": []any{}}, nil, 409)
	config["ss_network"] = "both"
	keyRequest(t, c, "PUT", uri, admin, body, nil, 409)
	config["ss_network"] = "tcp"
	entry["node_id"] = e.NodeID + 999
	keyRequest(t, c, "PUT", uri, admin, body, nil, 400)
	entry["node_id"] = e.NodeID
	entry["quota_bytes"] = 200
	keyRequest(t, c, "PUT", uri, admin, body, nil, 200)
	keyRequest(t, c, "POST", uri+"/revoke", admin, nil, nil, 200)
	keyRequest(t, c, "POST", uri+"/reissue", admin, nil, nil, 200)
	keyRequest(t, c, "GET", uri, readonly, nil, nil, 200)
	if after := get().Targets[0].Entries[0]; after.NodeID != e.NodeID || after.QuotaBytes != 200 || after.Snapshot.SSNetwork != "tcp" {
		t.Fatal("action changed entry identity")
	}
}

func TestShareTLSTransportBearerLifecycle(t *testing.T) {
	c, sid := natAPIClient(t)
	key, _ := issueTestAPIKey(t, c, 1, "read", "write")
	readOnly, _ := issueTestAPIKey(t, c, 1, "read")
	base := "/api/v1/shares"
	const version = agentproto.TLSTransportVersion
	report := func(v int) {
		t.Helper()
		c.do("POST", "/api/agent/v1/heartbeat", agentproto.Heartbeat{Version: "fixture", Metrics: agentproto.Metrics{Arch: "amd64"}, Diagnostics: agentproto.Diagnostics{TLSTransportVersion: v, SecurityPolicy: true, Nftables: true}}, 200)
	}
	ws := &nodecombo.WS{Path: "/entry", Host: "front.example.test"}
	grpc := &nodecombo.GRPC{ServiceName: "Share.Fixture"}
	entries := []domain.ShareEntry{
		{ID: "vless-tcp-tls", Snapshot: domain.ShareEntryConfig{Protocol: "vless", VLESS: &nodecombo.VLESS{Transport: "tcp", Security: "tls"}}},
		{ID: "vless-ws-tls", Snapshot: domain.ShareEntryConfig{Protocol: "vless", VLESS: &nodecombo.VLESS{Transport: "ws", Security: "tls", WS: ws}}},
		{ID: "vless-grpc-tls", Snapshot: domain.ShareEntryConfig{Protocol: "vless", VLESS: &nodecombo.VLESS{Transport: "grpc", Security: "tls", GRPC: grpc}}},
		{ID: "trojan-ws-tls", Snapshot: domain.ShareEntryConfig{Protocol: "trojan", Domain: "origin.example.test", CertMode: "external", CertID: "fixture-cert", Trojan: &nodecombo.Trojan{Transport: "ws", Security: "tls", WS: ws}}},
		{ID: "trojan-grpc-tls", Snapshot: domain.ShareEntryConfig{Protocol: "trojan", Trojan: &nodecombo.Trojan{Transport: "grpc", Security: "tls", GRPC: grpc}}},
		{ID: "trojan", Snapshot: domain.ShareEntryConfig{Protocol: "trojan"}}, // Existing explicit native snapshot, still compatible with old agents.
	}
	for i := range entries {
		entries[i].QuotaBytes = int64(100 + i)
	}
	body := map[string]any{"name": "fixture", "entry_version": 1, "targets": []domain.ShareTarget{{ServerID: sid, Entries: entries}}}
	report(0)
	keyRequest(t, c, "POST", base, readOnly, body, nil, 403)
	blocked, _ := keyRequest(t, c, "POST", base, key, body, nil, 409)
	if blocked["error"].(map[string]any)["code"] != "tls_transport_unavailable" {
		t.Fatal("wrong failure for old agent")
	}
	report(version)
	// Unknown or mismatched nested options may not be discarded on input.
	for _, snapshot := range []string{`{"protocol":"vless","trojan":{"transport":"ws","security":"tls"}}`, `{"protocol":"trojan","trojan":{"transport":"ws","security":"tls","ws":{"path":"/","headers":{}}}}`, `{"protocol":"trojan","trojan":{"transport":"grpc","security":"tls","grpc":{"service_name":"invalid/service"}}}`} {
		keyRequest(t, c, "POST", base, key, map[string]any{"name": "bad", "targets": []any{map[string]any{"server_id": sid, "entries": []any{map[string]any{"id": "bad", "snapshot": json.RawMessage(snapshot)}}}}}, nil, 400)
	}
	created, _ := keyRequest(t, c, "POST", base, key, body, nil, 201)
	id := int64(created["id"].(float64))
	uri := fmt.Sprintf("%s/%d", base, id)
	get := func() domain.Share {
		t.Helper()
		var sh domain.Share
		_, raw := keyRequest(t, c, "GET", uri, readOnly, nil, nil, 200)
		if err := json.Unmarshal(raw, &sh); err != nil {
			t.Fatal(err)
		}
		return sh
	}
	before := get()
	saved := before.Targets[0].Entries
	if len(saved) != len(entries) {
		t.Fatal("missing entries")
	}
	nodes := map[int64]domain.Node{}
	ports := map[int]bool{}
	server, err := c.api.Store.GetServer(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range saved {
		n, err := c.api.Store.GetNode(t.Context(), e.NodeID)
		want := entries[i].Snapshot
		if want.ManagedTLS() {
			if want.CertMode == "" {
				want.CertMode = server.CertMode
			}
			if want.Domain == "" {
				want.Domain = server.PublicHost
			}
		}
		if !reflect.DeepEqual(e.Snapshot, want) {
			a, _ := json.Marshal(e.Snapshot)
			b, _ := json.Marshal(want)
			t.Fatalf("entry %s: snapshot %s, want %s", e.ID, a, b)
		}
		if err != nil || e.ID != entries[i].ID || nodes[e.NodeID].ID != 0 || ports[n.ListenPort] || before.NodeQuota(n) != entries[i].QuotaBytes {
			t.Fatalf("entry %s: node %d, port %d, quota %d, want %d; err=%v", e.ID, e.NodeID, n.ListenPort, before.NodeQuota(n), entries[i].QuotaBytes, err)
		}
		nodes[e.NodeID] = n
		ports[n.ListenPort] = true
	}
	keyRequest(t, c, "POST", uri+"/pause", key, nil, nil, 200)
	keyRequest(t, c, "POST", uri+"/resume", key, nil, nil, 200)
	for nid, n := range nodes {
		current, err := c.api.Store.GetNode(t.Context(), nid)
		if err != nil || !bytes.Equal(current.Params, n.Params) || !bytes.Equal(current.ServerParams, n.ServerParams) {
			t.Fatal("pause/resume changed credentials or configuration", nid, err)
		}
	}
	entries[3].Snapshot.CertID = "replacement"
	keyRequest(t, c, "PUT", uri, key, body, nil, 409)
	entries[3].Snapshot.CertID = "fixture-cert"
	entries[3].Snapshot.Domain = "changed.example.test"
	keyRequest(t, c, "PUT", uri, key, body, nil, 409)
	entries[3].Snapshot.Domain = "origin.example.test"
	ws.Path = "/different"
	keyRequest(t, c, "PUT", uri, key, body, nil, 409)
	ws.Path = "/entry"
	grpc.ServiceName = "Other"
	keyRequest(t, c, "PUT", uri, key, body, nil, 409)
	grpc.ServiceName = "Share.Fixture"
	for i := range saved {
		entries[i].NodeID = saved[i].NodeID
	}
	entries[3].QuotaBytes = 500
	keyRequest(t, c, "PUT", uri, key, body, nil, 200)
	if _, err := c.api.Store.DB().Exec(`UPDATE shares SET node_usage=?,used_upload=500 WHERE id=?`, fmt.Sprintf(`{"%d":[500,0]}`, entries[3].NodeID), id); err != nil {
		t.Fatal(err)
	}
	keyRequest(t, c, "POST", uri+"/revoke", key, nil, nil, 200)
	report(0)
	keyRequest(t, c, "POST", uri+"/reissue", key, nil, nil, 409)
	if get().Status != domain.ShareRevoked {
		t.Fatal("failed reissue restored access")
	}
	for nid, n := range nodes {
		after, err := c.api.Store.GetNode(t.Context(), nid)
		if err != nil || !after.Revoked || !bytes.Equal(after.Params, n.Params) {
			t.Fatal("failed reissue changed credentials", err)
		}
	}
	report(version)
	keyRequest(t, c, "POST", uri+"/reissue", key, nil, nil, 200)
	after := get()
	saved[3].QuotaBytes = 500
	if !reflect.DeepEqual(saved, after.Targets[0].Entries) || after.UsedUpload != 500 {
		t.Fatal("reissue lost IDs, usage, quota or configuration")
	}
	for nid, n := range nodes {
		current, err := c.api.Store.GetNode(t.Context(), nid)
		if err != nil || current.Revoked || current.ListenPort != n.ListenPort || bytes.Equal(current.Params, n.Params) || after.NodeExhausted(current) != (nid == entries[3].NodeID) {
			t.Fatalf("entry %d: rotation, port or quota isolation failed: %v", nid, err)
		}
	}
}
