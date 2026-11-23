package agent

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"strings"
	"testing"
)

func TestTLSTransportIntentAndCertificateRejectionReceipts(t *testing.T) {
	n := agentproto.NodeSpec{NodeID: 1, Protocol: "vless", Core: "singbox", ListenPort: 21001, Params: map[string]any{"security": "tls", "network": "ws", "ws_path": "/app", "uuid": "00000000-0000-4000-8000-000000000001", "cert_mode": "external", "cert_id": "fixture", "tls_domain": "origin.example.org"}, Cert: &agentproto.CertSpec{Mode: "external", ID: "fixture", Domain: "origin.example.org"}}
	ds := &agentproto.DesiredState{Revision: 2, ServerID: 1, TLSTransportVersion: 1, Nodes: []agentproto.NodeSpec{n}}
	verify := func(d *agentproto.DesiredState, ok bool) {
		t.Helper()
		d.Hash = agentproto.ContentHash(d)
		if err := agentproto.ValidateDesired(d, 1, 0, ""); (err == nil) != ok {
			t.Fatalf("valid=%v want=%v err=%v", err == nil, ok, err)
		}
	}
	for _, mutate := range []func(*agentproto.NodeSpec){func(n *agentproto.NodeSpec) { n.Cert = nil }, func(n *agentproto.NodeSpec) { n.Cert.ID = "other" }, func(n *agentproto.NodeSpec) { n.Cert.Domain = "other.example.org" }, func(n *agentproto.NodeSpec) { n.Cert.Mode = "self_signed" }, func(n *agentproto.NodeSpec) { n.Cert.CertPath = "/etc/fixture" }, func(n *agentproto.NodeSpec) { n.Network = &agentproto.NodeNetworkSpec{} }} {
		x := n
		c := *x.Cert
		x.Cert = &c
		mutate(&x)
		bad := *ds
		bad.Nodes = []agentproto.NodeSpec{x}
		verify(&bad, false)
	}
	verify(ds, true)
	a := &Agent{StateDir: t.TempDir(), State: &State{ServerURL: "https://fixture.example.org", AgentToken: "fixture", ServerID: 1}, Drivers: map[string]core.Driver{"singbox": &applicationFinalizer{}}}
	if err := a.rememberNetworkIntent(ds); err != nil {
		t.Fatal(err)
	}
	a.rejectTLSCertificate(t.Context(), ds, n, false)
	st, err := LoadState(a.StateDir)
	if err != nil || st.NodeApplicationSequence != 1 || len(st.NodeApplications) != 1 || st.NodeApplications[0].Status != "failed" || st.NodeApplications[0].TLSTransportVersion != 0 || strings.Contains(st.ApplyError, "fixture") {
		t.Fatal("invalid or secret-bearing certificate refusal", err)
	}
	if err := a.finishNodeApplications(ds, ds.Nodes, map[string]bool{"singbox": true}, map[string]bool{"singbox": true}); err != nil {
		t.Fatal(err)
	}
	rr := a.State.NodeApplications[0]
	if rr.Status != "applied" || rr.TLSTransportVersion != 1 || a.State.NodeApplicationSequence != 2 {
		t.Fatal("recovery omitted explicit proof")
	}
	cleanup := *ds
	cleanup.Nodes = nil
	cleanup.Revision++
	cleanup.Hash = agentproto.ContentHash(&cleanup)
	if err := a.rememberNetworkIntent(&cleanup); err != nil {
		t.Fatal(err)
	}
	a.State, err = LoadState(a.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	rev, hash := a.State.desiredBoundary()
	if err = agentproto.ValidateDesired(ds, 1, rev, hash); err == nil {
		t.Fatal("older pre-cleanup revision accepted")
	}
	cleanup.Revision++
	cleanup.TLSTransportVersion = 0
	cleanup.Hash = agentproto.ContentHash(&cleanup)
	if err = a.rememberNetworkIntent(&cleanup); err == nil {
		t.Fatal("cleanup erased sticky TLS transport floor")
	}
}
