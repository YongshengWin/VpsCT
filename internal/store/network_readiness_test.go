package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func readyNetworkFixture(t *testing.T, s *Store) (NodeNetworkRequest, agentproto.Diagnostics, *agentproto.NetworkSnapshot) {
	t.Helper()
	ctx := context.Background()
	server, p, n := egressFixture(t, s)
	n.ServerParams = []byte(`{"method":"2022-blake3-aes-128-gcm"}`)
	if err := s.UpdateNodeCredentials(ctx, &n); err != nil {
		t.Fatal(err)
	}
	d := agentproto.Diagnostics{NetworkBindingVersion: 1, NetworkConfigureAllowed: true, SecurityVersion: 1, SecurityPolicy: true, Nftables: true, Systemd: true,
		Cores: []agentproto.CoreStatus{{Name: "sing-box", Version: domain.DefaultSingBoxVersion, Installed: true}}}
	writeNetworkDiagnostics(t, s, server.ID, d)
	snapshot := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("b", 32), BootID: "boot", Sequence: 1, SampledAt: s.Now().Add(-time.Minute), Status: "ok",
		Interfaces: []agentproto.NetworkInterface{{ID: strings.Repeat("1", 32), Generation: strings.Repeat("c", 32), Name: "wan0", Index: 2, Kind: "physical", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}
	if err := s.IngestNetwork(ctx, server.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	return NodeNetworkRequest{ID: strings.Repeat("a", 32), NodeID: n.ID, Network: nodePolicy(p)}, d, snapshot
}

func writeNetworkDiagnostics(t *testing.T, s *Store, sid int64, d agentproto.Diagnostics) {
	t.Helper()
	b, _ := json.Marshal(d)
	if _, err := s.db.Exec(`UPDATE agents SET token_hash='fixture-hash',last_seen_at=?,diagnostics=?,metrics='{"arch":"arm64"}' WHERE server_id=?`, fmtTime(s.Now()), string(b), sid); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkReadinessRequiresSupportedArchitectureButAllowsCleanup(t *testing.T) {
	for _, arch := range []string{"", "386", "unrecognized"} {
		t.Run(arch, func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			in, _, _ := readyNetworkFixture(t, s)
			n, _ := s.GetNode(ctx, in.NodeID)
			metrics, _ := json.Marshal(agentproto.Metrics{Arch: arch})
			if _, err := s.db.Exec(`UPDATE agents SET metrics=? WHERE server_id=?`, string(metrics), *n.ServerID); err != nil {
				t.Fatal(err)
			}
			v, err := s.PreviewNodeNetwork(ctx, n.ID, in.Network)
			if err != nil || v.Ready || v.Architecture != arch {
				t.Fatal(v, err)
			}
			if _, err := s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkNotReady) {
				t.Fatal(err)
			}
			v, err = s.PreviewNodeNetwork(ctx, n.ID, nil)
			if err != nil || !v.Ready {
				t.Fatal("architecture blocked explicit cleanup", v, err)
			}
		})
	}
}

func TestNetworkReadinessAMD64StillRequiresHostCapabilities(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, diag, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	if _, err := s.db.Exec(`UPDATE agents SET metrics='{"arch":"amd64"}' WHERE server_id=?`, *n.ServerID); err != nil {
		t.Fatal(err)
	}
	v, err := s.PreviewNodeNetwork(ctx, n.ID, in.Network)
	if err != nil || !v.Ready {
		t.Fatal(v, err)
	}
	diag.Nftables = false
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	v, err = s.PreviewNodeNetwork(ctx, n.ID, in.Network)
	if err != nil || v.Ready {
		t.Fatal("AMD64 bypassed host prerequisite", v, err)
	}
}

func TestSOCKS5ReadinessAndCleanupRequireEgressCapability(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, diag, _ := readyNetworkFixture(t, s)
	n, err := s.GetNode(ctx, in.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := networkconfig.SOCKS5{Server: "192.0.2.2", ServerPort: 1080, Authentication: "none", Family: "dual", ConnectTimeoutSeconds: 10, DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}, Outer: networkconfig.Direct{Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}}}
	p := domain.EgressProfile{ServerID: *n.ServerID, Name: "SOCKS fixture", Kind: "socks5", Enabled: true}
	raw, _ := json.Marshal(cfg)
	if err := s.CreateEgressProfile(ctx, &p, raw); err != nil {
		t.Fatal(err)
	}
	in.Network.EgressProfileID = p.ID
	v, err := s.PreviewNodeNetwork(ctx, n.ID, in.Network)
	if err != nil || v.Ready {
		t.Fatal("old agent admitted a transit binding", v, err)
	}
	diag.NetworkEgressVersion = agentproto.NetworkEgressVersion
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	v, err = s.PreviewNodeNetwork(ctx, n.ID, in.Network)
	if err != nil || !v.Ready || v.EgressVersion != 1 {
		t.Fatal("qualified SOCKS binding rejected", v, err)
	}
	if _, err := s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	diag.NetworkEgressVersion = 0
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	v, err = s.PreviewNodeNetwork(ctx, n.ID, nil)
	if err != nil || v.Ready {
		t.Fatal("direct-only agent was allowed transit cleanup", v, err)
	}
	diag.NetworkEgressVersion = 1
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	for _, server := range []string{"10.0.0.1", "192.0.2.1", "upstream.example"} {
		cfg.Server = server
		if server == "upstream.example" {
			cfg.Outer.DNS.Address = "127.0.0.1"
		}
		raw, _ = json.Marshal(cfg)
		if server == "upstream.example" {
			// Structural resolver validation also rejects loopback before storage.
			if _, err = s.AppendEgressRevision(ctx, p.ID, p.CurrentRevision, p.Name, true, raw); err == nil {
				t.Fatal("loopback bootstrap resolver saved")
			}
			continue
		}
		p, err = s.AppendEgressRevision(ctx, p.ID, p.CurrentRevision, p.Name, true, raw)
		if err != nil {
			t.Fatal(err)
		}
		in.Network.EgressRevision = p.CurrentRevision
		v, err = s.PreviewNodeNetwork(ctx, n.ID, in.Network)
		if err != nil || v.Ready {
			t.Fatal("unsafe upstream admitted", server, v, err)
		}
	}
}

func TestNetworkReadinessUsesObservedCapabilitiesAndAtomicAdmission(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, diag, snapshot := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	before := snapshot.SampledAt
	view, err := s.PreviewNodeNetwork(ctx, n.ID, in.Network)
	if err != nil || !view.Ready || !view.RequiresLocalValidation || view.PinnedCoreVersion != domain.DefaultSingBoxVersion {
		t.Fatal(view, err)
	}
	if !snapshot.SampledAt.Equal(before) {
		t.Fatal("preview changed the reported sample time")
	}
	// A preview is not a reservation: the transactional writer checks again.
	diag.SecurityPaused = true
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	if _, err = s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkNotReady) {
		t.Fatal("stale successful preview bypassed current policy", err)
	}
	if gen, _ := s.NetworkGeneration(ctx, *n.ServerID); gen != 0 {
		t.Fatal("failed readiness check changed desired generation")
	}
	diag.SecurityPaused = false
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	op, err := s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil || op.Status != "queued" {
		t.Fatal(op, err)
	}
	// Once accepted, an exact retry remains idempotent even after going offline.
	if _, err = s.db.Exec(`UPDATE agents SET last_seen_at=NULL WHERE server_id=?`, *n.ServerID); err != nil {
		t.Fatal(err)
	}
	retried, err := s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil || retried.ID != op.ID || retried.ResourceRevision != 1 {
		t.Fatal("offline observation broke accepted idempotent request", err)
	}
}

func TestNetworkReadinessRejectsMissingPrerequisitesWithoutExposingRawErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*agentproto.Diagnostics)
	}{
		{"binding_protocol", func(d *agentproto.Diagnostics) { d.NetworkBindingVersion = 0 }},
		{"local_policy", func(d *agentproto.Diagnostics) { d.SecurityPolicy = false }},
		{"paused", func(d *agentproto.Diagnostics) { d.SecurityPaused = true }},
		{"permission", func(d *agentproto.Diagnostics) { d.NetworkConfigureAllowed = false }},
		{"systemd", func(d *agentproto.Diagnostics) { d.Systemd = false }},
		{"nftables", func(d *agentproto.Diagnostics) { d.Nftables = false }},
		{"metering", func(d *agentproto.Diagnostics) { d.MeteringError = "fixture-private-error" }},
		{"guard", func(d *agentproto.Diagnostics) { d.NetworkGuardError = "fixture-private-error" }},
		{"version", func(d *agentproto.Diagnostics) { d.Cores[0].Version = "1.11.0" }},
		{"uninstalled", func(d *agentproto.Diagnostics) { d.Cores[0].Installed = false }},
		{"duplicate", func(d *agentproto.Diagnostics) {
			d.Cores = append([]agentproto.CoreStatus{{Name: "sing-box"}}, d.Cores...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			in, diag, _ := readyNetworkFixture(t, s)
			n, _ := s.GetNode(context.Background(), in.NodeID)
			tc.edit(&diag)
			writeNetworkDiagnostics(t, s, *n.ServerID, diag)
			v, err := s.PreviewNodeNetwork(context.Background(), n.ID, in.Network)
			if err != nil || v.Ready {
				t.Fatal("unsupported combination admitted", v, err)
			}
			b, _ := json.Marshal(v)
			if strings.Contains(string(b), "fixture-private-error") || strings.Contains(string(b), "token_hash") {
				t.Fatal("readiness leaked diagnostic or credential details")
			}
		})
	}
}

func TestNetworkReadinessRejectsOldInventoryAndAllowsExplicitResetOfLostNIC(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, diag, snapshot := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	if _, err := s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	// The old address cannot be rebound after moving to another interface.
	snapshot.Sequence++
	snapshot.Interfaces[0].ID = strings.Repeat("2", 32)
	if err := s.IngestNetwork(ctx, *n.ServerID, snapshot); err != nil {
		t.Fatal(err)
	}
	v, err := s.PreviewNodeNetwork(ctx, n.ID, in.Network)
	if err != nil || v.Ready {
		t.Fatal("missing original NIC admitted", err)
	}
	reset := NodeNetworkRequest{ID: strings.Repeat("d", 32), NodeID: n.ID, ExpectedRevision: 1}
	if _, err := s.RequestReadyNodeNetwork(ctx, reset, domain.AuditEvent{}); err != nil {
		t.Fatal("lost NIC prevented explicit reset", err)
	}
	// Controller receipt freshness is independent of the agent's sample clock.
	if _, err = s.db.Exec(`UPDATE network_snapshots SET received_at=? WHERE server_id=?`, fmtTime(s.Now().Add(-3*time.Minute)), *n.ServerID); err != nil {
		t.Fatal(err)
	}
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	v, err = s.NetworkCapabilities(ctx, *n.ServerID)
	if err != nil || v.Ready {
		t.Fatal("fresh heartbeat revived stale inventory", err)
	}
	// A source address has to remain under its exact interface identity.
	policy := &networkconfig.Node{ListenMode: "address", ListenAddress: "192.0.2.1", ListenInterfaceID: strings.Repeat("1", 32), AdvertiseMode: "inherit", OnUnavailable: "block"}
	v, err = s.PreviewNodeNetwork(ctx, n.ID, policy)
	if err != nil || v.Ready {
		t.Fatal("stale/moved source was considered ready", err)
	}
}

func TestStandaloneListenBindingAndVersionIsolation(t *testing.T) {
	for _, core := range []domain.Core{domain.CoreSnell, domain.CoreMita} {
		t.Run(string(core), func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			in, diag, _ := readyNetworkFixture(t, s)
			n, err := s.GetNode(ctx, in.NodeID)
			if err != nil {
				t.Fatal(err)
			}
			_, version := bindingCoreSetting(core)
			if _, err = s.db.Exec(`UPDATE nodes SET core=?,protocol=? WHERE id=?`, core, string(core), n.ID); err != nil {
				t.Fatal(err)
			}
			// No sing-box installation is needed for an independent listener.
			diag.Cores = []agentproto.CoreStatus{{Name: agentproto.CoreVersionKey(string(core)), Version: version, Installed: true}}
			in.Network.EgressProfileID, in.Network.EgressRevision = 0, 0
			writeNetworkDiagnostics(t, s, *n.ServerID, diag)
			view, err := s.PreviewNodeNetwork(ctx, n.ID, in.Network)
			if err != nil || view.Ready {
				t.Fatal("older agent admitted listening binding", view, err)
			}
			diag.ListenBindingVersion = 1
			writeNetworkDiagnostics(t, s, *n.ServerID, diag)
			view, err = s.PreviewNodeNetwork(ctx, n.ID, in.Network)
			if err != nil || !view.Ready {
				t.Fatal(view, err)
			}
			if _, err = s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); err != nil {
				t.Fatal(err)
			}
			key, _ := bindingCoreSetting(core)
			if err = s.SetSettings(ctx, map[string]string{key: "99.0.0"}); !errors.Is(err, ErrNetworkCoreVersion) {
				t.Fatal("incompatible listener upgrade", err)
			}
			if err = s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: "99.0.0"}); err != nil {
				t.Fatal("unrelated core pin blocked", err)
			}
			in.Network.EgressProfileID, in.Network.EgressRevision = 1, 1
			if _, err = s.SetNodeNetwork(ctx, n.ID, 1, in.Network, nil); !errors.Is(err, ErrNetworkCoreVersion) {
				t.Fatal("standalone egress accepted", err)
			}
		})
	}
}
