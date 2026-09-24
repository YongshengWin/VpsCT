//go:build linux

package main

import (
	"context"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
	"encoding/json"
	"fmt"
	"os"
)

// One focused shared-process case qualifies the additional TLS inbounds for
// direct binding; it does not claim transport-outbound compatibility.
func testProtocolBindings(ctx context.Context, st *store.Store, d *desired.Builder, server domain.Server, interfaces map[string]string, admin *fixtureAdmin) {
	dns := networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.10", Port: 15353}
	profile := domain.EgressProfile{ServerID: server.ID, Name: "TLS direct fixture", Kind: "direct", Enabled: true}
	raw, _ := json.Marshal(networkconfig.Direct{InterfaceID: interfaces["wan1"], Family: "dual", DNS: dns})
	must(st.CreateEgressProfile(ctx, &profile, raw))
	var nodes []domain.Node
	protocols := []string{"trojan", "anytls", "hysteria2", "tuic"}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "reality" {
		protocols = []string{"vless"}
		run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=reality.fixture", "-addext", "subjectAltName=DNS:reality.fixture", "-keyout", "/tmp/reality-fixture.key", "-out", "/tmp/reality-fixture.crt")
		stop := process(ctx, "ip", "netns", "exec", "landing", "openssl", "s_server", "-accept", "203.0.113.10:443", "-key", "/tmp/reality-fixture.key", "-cert", "/tmp/reality-fixture.crt", "-tls1_3", "-alpn", "h2", "-www")
		defer stop()
	}
	for i, protocol := range protocols {
		var n domain.Node
		admin.request("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", server.ID), map[string]any{"protocol": protocol, "name": protocol + " binding fixture", "port": 25200 + i, "sni": "reality.fixture"}, 201, &n)
		policy := &networkconfig.Node{ListenMode: "address", ListenAddress: "192.0.2.1", ListenInterfaceID: interfaces["wan0"], AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: profile.ID, EgressRevision: 1}
		n, e := st.SetNodeNetwork(ctx, n.ID, 0, policy, nil)
		must(e)
		nodes = append(nodes, n)
	}
	must(d.ReconcileNetworkOperations(ctx))
	rec, e := st.LatestDesiredState(ctx, server.ID)
	must(e)
	eventually("TLS inbounds bound", func() bool {
		a, e := st.GetAgentByServer(ctx, server.ID)
		return e == nil && a.ApplyError == "" && a.AppliedRevision == rec.Revision
	})
	for i, n := range nodes {
		stop := startClient(ctx, i, n)
		defer stop()
		eventually(n.Protocol+" client", func() bool { got, e := probe(i, "tcp", "203.0.113.10"); return e == nil && got == "198.51.100.1" })
		for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10", "binding.test"} {
			want := "198.51.100.1"
			if target == "2001:db8:ffff::10" {
				want = "2001:db8:2::1"
			}
			for _, transport := range []string{"tcp", "greeting", "udp"} {
				got, e := probe(i, transport, target)
				if e != nil || got != want {
					panic(fmt.Sprintf("%s %s %s: %v", n.Protocol, transport, target, e))
				}
			}
		}
		fmt.Println("PASS", n.Protocol, "bound listen + second WAN + TCP/UDP/server-first + IPv4/IPv6/DNS")
	}
	run("ip", "link", "set", "wan1", "down")
	eventually("selected WAN failure fenced", func() bool {
		a, e := st.GetAgentByServer(ctx, server.ID)
		if e != nil {
			return false
		}
		var diag struct {
			Errors map[int64]string `json:"network_binding_errors"`
		}
		_ = json.Unmarshal(a.Diagnostics, &diag)
		return len(diag.Errors) >= len(nodes)
	})
	for i, n := range nodes {
		if _, e := probe(i, "tcp", "203.0.113.10"); e == nil {
			panic(n.Protocol + " fell back to another WAN")
		}
	}
	fmt.Println("PASS all TLS direct bindings block after selected interface failure")
}
