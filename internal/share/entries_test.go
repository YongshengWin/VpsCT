package share

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/nodecombo"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
)

func enrollEntryAgent(t *testing.T, st *store.Store, srv domain.Server, now time.Time) domain.Agent {
	t.Helper()
	ctx := t.Context()
	a, err := st.GetAgentByServer(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentEnrollToken(ctx, srv.ID, "fixture", now.Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteEnrollment(ctx, a.ID, "fixture", "fixture-agent", "fixture"); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestShareComboIdentityQuotaAndReissue(t *testing.T) {
	m, st, srv, now := setup(t)
	ctx := t.Context()
	a := enrollEntryAgent(t, st, srv, *now)
	diagnostics, _ := json.Marshal(agentproto.Diagnostics{XrayVersion: agentproto.XrayVersion, Systemd: true, Nftables: true, SecurityPolicy: true})
	if err := st.Heartbeat(ctx, a.ID, "fixture", "", "", []byte(`{"arch":"amd64"}`), diagnostics); err != nil {
		t.Fatal(err)
	}
	x := domain.ShareEntry{ID: "reality-xhttp", QuotaBytes: 100, Snapshot: domain.ShareEntryConfig{Protocol: "vless", VLESS: &nodecombo.VLESS{Transport: "xhttp", XHTTP: &nodecombo.XHTTP{Path: "/one", Mode: "packet-up", Host: "edge.example.test"}}}}
	sh := domain.Share{Name: "combo", Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}, QuotaBytes: map[string]int64{"vless": 700}, Entries: []domain.ShareEntry{x}}}}
	first, err := m.Create(ctx, &sh)
	if err != nil {
		t.Fatal(err)
	}
	entries := func() (domain.Node, domain.Node) {
		t.Helper()
		ns, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID, IncludeRevoked: true})
		if err != nil || len(ns) != 2 {
			t.Fatalf("wrong entries: count=%d err=%v", len(ns), err)
		}
		if ns[0].Core == domain.CoreXray {
			return ns[1], ns[0]
		}
		return ns[0], ns[1]
	}
	tcp, xhttp := entries()
	if tcp.Protocol != xhttp.Protocol || tcp.ID == xhttp.ID || tcp.ListenPort == xhttp.ListenPort || xhttp.ID != sh.Targets[0].Entries[0].NodeID || xhttp.Core != domain.CoreXray || tcp.Core != domain.CoreSingBox {
		t.Fatal("colliding identities or ports")
	}
	if sh.NodeQuota(tcp) != 700 || sh.NodeQuota(xhttp) != 100 {
		t.Fatal("limits collided")
	}
	before, err := st.NetworkGeneration(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE shares SET node_usage=?,used_upload=100 WHERE id=?`, fmt.Sprintf(`{"%d":[100,0]}`, xhttp.ID), sh.ID); err != nil {
		t.Fatal(err)
	}
	after, err := st.NetworkGeneration(ctx, srv.ID)
	if err != nil || after <= before {
		t.Fatal("entry crossing did not fence old publication", err)
	}
	load := func() domain.Share {
		t.Helper()
		v, err := st.GetShare(ctx, sh.ID)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	sh = load()
	if !sh.NodeExhausted(xhttp) || sh.NodeExhausted(tcp) {
		t.Fatal("wrong entry exhausted")
	}
	if err := m.Pause(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Resume(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	t1, x1 := entries()
	if !bytes.Equal(tcp.Params, t1.Params) || !bytes.Equal(xhttp.Params, x1.Params) {
		t.Fatal("pause/resume rotated credentials")
	}
	if err := m.Revoke(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Heartbeat(ctx, a.ID, "fixture", "", "", []byte(`{}`), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reissue(ctx, sh.ID); !errors.Is(err, store.ErrXrayUnavailable) {
		t.Fatalf("missing host capability was not rejected: %v", err)
	}
	t1, x1 = entries()
	sub, err := st.GetSubscriptionByShare(ctx, sh.ID)
	if err != nil || sub.Enabled || sub.Token != first || !t1.Revoked || !x1.Revoked || !bytes.Equal(t1.Params, tcp.Params) || !bytes.Equal(x1.Params, xhttp.Params) {
		t.Fatal("failed reissue partially changed credentials or access", err)
	}
	if err := st.Heartbeat(ctx, a.ID, "fixture", "", "", []byte(`{"arch":"amd64"}`), diagnostics); err != nil {
		t.Fatal(err)
	}
	if next, err := m.Reissue(ctx, sh.ID); err != nil || next == first {
		t.Fatal("reissue", err)
	}
	t1, x1 = entries()
	sh = load()
	v, err := provision.VLESSFromNode(x1)
	if err != nil || !reflect.DeepEqual(v, sh.Targets[0].Entries[0].Snapshot.VLESS) || t1.ID != tcp.ID || x1.ID != xhttp.ID || x1.ListenPort != xhttp.ListenPort || bytes.Equal(t1.Params, tcp.Params) || bytes.Equal(x1.Params, xhttp.Params) || !sh.NodeExhausted(x1) || sh.UsedUpload != 100 {
		t.Fatal("successful reissue changed snapshot, identity or accounting", err)
	}
	sh.Targets[0].Entries = nil
	if err := m.Update(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	_, old := entries()
	if !old.Revoked {
		t.Fatal("removed transport still live")
	}
	if _, err := m.Reissue(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	_, old = entries()
	if !old.Revoked || !bytes.Equal(old.Params, x1.Params) {
		t.Fatal("reissue resurrected a removed entry")
	}
	x.Snapshot.VLESS.XHTTP.Path = "/different"
	sh.Targets[0].Entries = []domain.ShareEntry{x}
	if err := m.Update(ctx, &sh); !errors.Is(err, store.ErrShareSnapshot) {
		t.Fatal("old identity reused for a different path", err)
	}
	sh.Targets[0].Entries[0].Snapshot.VLESS.XHTTP.Path = "/one"
	if err := m.Update(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	_, restored := entries()
	if restored.Revoked || restored.ID != xhttp.ID || restored.ListenPort != xhttp.ListenPort || !sh.NodeExhausted(restored) {
		t.Fatal("re-selection reset the dedicated entry")
	}
}

func TestShareEntryRestartKeepsCanonicalSnapshot(t *testing.T) {
	m, st, srv, now := setup(t)
	ctx := t.Context()
	srv.CertMode = "external"
	if err := st.UpdateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	a := enrollEntryAgent(t, st, srv, *now)
	diagnostics, _ := json.Marshal(agentproto.Diagnostics{TLSTransportVersion: agentproto.TLSTransportVersion, SecurityPolicy: true, Nftables: true})
	if err := st.Heartbeat(ctx, a.ID, "fixture", "", "", []byte(`{"arch":"amd64"}`), diagnostics); err != nil {
		t.Fatal(err)
	}
	entries := []domain.ShareEntry{
		{ID: "ss-both", Snapshot: domain.ShareEntryConfig{Protocol: "ss", SSMethod: "aes-256-gcm", SSNetwork: "both"}, QuotaBytes: 30},
		{ID: "vless-ws", Snapshot: domain.ShareEntryConfig{Protocol: "vless", VLESS: &nodecombo.VLESS{Transport: "ws", Security: "tls", WS: &nodecombo.WS{Path: "/fixture", Host: "front.example.test"}}, Domain: "origin.example.test", CertID: "registered-cert"}, QuotaBytes: 31},
		{ID: "trojan-grpc", Snapshot: domain.ShareEntryConfig{Protocol: "trojan", Trojan: &nodecombo.Trojan{Transport: "grpc", Security: "tls", GRPC: &nodecombo.GRPC{ServiceName: "fixture.share"}}, CertID: "registered-cert"}, QuotaBytes: 32},
	}
	sh := domain.Share{Name: "restart", Targets: []domain.ShareTarget{{ServerID: srv.ID, Entries: entries}}}
	token, err := m.Create(ctx, &sh)
	if err != nil {
		t.Fatal(err)
	}
	before := make([]domain.Node, len(entries))
	for i, e := range entries {
		before[i], err = st.GetNode(ctx, e.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && (e.Snapshot.CertMode != "external" || e.Snapshot.CertID != "registered-cert" || !matchesSnapshot(before[i], e.Snapshot)) {
			t.Fatal("inherited certificate was not pinned", e.ID)
		}
	}
	if entries[1].Snapshot.Domain != "origin.example.test" || entries[2].Snapshot.Domain != srv.PublicHost {
		t.Fatal("TLS certificate name did not default independently from WS Host")
	}
	// A server policy change must not reinterpret prior entries on restart.
	srv.CertMode = "self_signed"
	srv.PublicHost = "replacement.example.test"
	if err := st.UpdateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := st.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	again.Now = st.Now
	manager := New(again, desired.New(again))
	loaded, err := again.GetShare(ctx, sh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entries, loaded.Targets[0].Entries) {
		t.Fatal("lost stored combination")
	}
	if err := manager.EnsureNodes(ctx, &loaded); err != nil {
		t.Fatal(err)
	}
	for _, old := range before {
		n, err := again.GetNode(ctx, old.ID)
		if err != nil || !bytes.Equal(old.Params, n.Params) || !bytes.Equal(old.ServerParams, n.ServerParams) || old.ListenPort != n.ListenPort {
			t.Fatal("restart rotated or moved entry", err)
		}
	}
	sub, err := again.GetSubscriptionByShare(ctx, sh.ID)
	if err != nil || sub.Token != token {
		t.Fatal("restart rotated subscription", err)
	}
	if next, err := manager.Reissue(ctx, sh.ID); err != nil || next == token {
		t.Fatal("reissue after defaults changed", err)
	}
	for i, e := range entries {
		n, err := again.GetNode(ctx, e.NodeID)
		if err != nil || !matchesSnapshot(n, e.Snapshot) || n.ListenPort != before[i].ListenPort || bytes.Equal(n.Params, before[i].Params) {
			t.Fatal("rotation after restart changed immutable options", e.ID, err)
		}
	}
	// Even a consistent client/server change cannot change the identity that
	// was bound to this entry. A different local certificate alias is drift.
	n, err := again.GetNode(ctx, entries[1].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	if err := json.Unmarshal(n.ServerParams, &params); err != nil {
		t.Fatal(err)
	}
	params["cert_id"] = "alternate-cert"
	n.ServerParams, err = json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.UpdateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reissue(ctx, sh.ID); !errors.Is(err, store.ErrShareSnapshot) {
		t.Fatal("certificate drift accepted", err)
	}
}
