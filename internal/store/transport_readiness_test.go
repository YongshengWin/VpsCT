package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestPrivateSOCKSReadinessRequiresExactReportedAuthority(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream", true: "bootstrap"}[bootstrap], func(t *testing.T) {
			s, ctx := openTest(t), context.Background()
			in, diag, snapshot := readyNetworkFixture(t, s)
			n, _ := s.GetNode(ctx, in.NodeID)
			cfg := networkconfig.SOCKS5{Server: "10.23.0.2", ServerPort: 1080, Authentication: "none", UDP: true, Family: "dual", ConnectTimeoutSeconds: 10,
				DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}, Outer: networkconfig.Direct{Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}}}
			purpose, port := "socks5", 1080
			if bootstrap {
				cfg.Server = "upstream.example"
				cfg.Outer.DNS = networkconfig.Resolver{Transport: "udp", Address: "10.23.0.2", Port: 15353}
				purpose, port = "bootstrap_dns", 15353
			}
			p := domain.EgressProfile{ServerID: *n.ServerID, Name: "private fixture", Kind: "socks5", Enabled: true}
			raw, _ := json.Marshal(cfg)
			if err := s.CreateEgressProfile(ctx, &p, raw); err != nil {
				t.Fatal(err)
			}
			in.Network.EgressProfileID, in.Network.EgressRevision = p.ID, 1
			diag.NetworkEgressVersion = agentproto.NetworkEgressVersion
			diag.NetworkTransportVersion = agentproto.NetworkTransportVersion
			grants := []networkconfig.TransportGrant{
				{NodeID: n.ID, EgressProfileID: p.ID, Purpose: purpose, Network: "tcp", Address: "10.23.0.2", Port: port},
				{NodeID: n.ID, EgressProfileID: p.ID, Purpose: purpose, Network: "udp", Address: "10.23.0.2", Port: port},
			}
			check := func(want bool) {
				t.Helper()
				writeNetworkDiagnostics(t, s, *n.ServerID, diag)
				v, err := s.PreviewNodeNetwork(ctx, n.ID, in.Network)
				if err != nil || v.Ready != want {
					t.Fatalf("ready=%v want=%v: %+v %v", v.Ready, want, v.Checks, err)
				}
			}
			check(false)
			for _, change := range []func(*networkconfig.TransportGrant){
				func(g *networkconfig.TransportGrant) { g.NodeID++ },
				func(g *networkconfig.TransportGrant) { g.EgressProfileID++ },
				func(g *networkconfig.TransportGrant) { g.Address = "10.23.0.3" },
				func(g *networkconfig.TransportGrant) { g.Port++ },
				func(g *networkconfig.TransportGrant) {
					g.Purpose = map[string]string{"socks5": "bootstrap_dns", "bootstrap_dns": "socks5"}[g.Purpose]
				},
			} {
				diag.TransportGrants = append([]networkconfig.TransportGrant(nil), grants...)
				change(&diag.TransportGrants[0])
				check(false)
			}
			diag.TransportGrants = grants[:1]
			check(false) // UDP needs its own authorization, including DNS fallback.
			diag.TransportGrants = grants
			diag.NetworkTransportVersion = 0
			check(false)
			diag.NetworkTransportVersion = agentproto.NetworkTransportVersion
			check(true)
			// A successful preview cannot reserve subsequently revoked authority.
			diag.TransportGrants = nil
			writeNetworkDiagnostics(t, s, *n.ServerID, diag)
			if _, err := s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkNotReady) {
				t.Fatal("revoked permission bypassed the transactional recheck", err)
			}
			diag.TransportGrants = grants
			snapshot.Sequence++
			snapshot.Interfaces[0].Addresses = append(snapshot.Interfaces[0].Addresses, "10.23.0.2/24")
			if err := s.IngestNetwork(ctx, *n.ServerID, snapshot); err != nil {
				t.Fatal(err)
			}
			check(false) // local endpoint forbidden even with an exact root grant.
		})
	}
}
