package store

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestManagedTransitIsolationAndRetirement(t *testing.T) {
	managedTransitLifecycle(t, "198.51.100.2", "wireguard")
}
func TestManagedTransitAddressAuthorization(t *testing.T) {
	for _, host := range []string{"10.44.0.2", "landing.example"} {
		t.Run(host, func(t *testing.T) { managedTransitLifecycle(t, host, "wireguard") })
	}
}
func TestManagedSS2022TransitIsolationAndRetirement(t *testing.T) {
	managedTransitLifecycle(t, "198.51.100.2", "ss2022")
}
func managedTransitLifecycle(t *testing.T, host, protocol string) {
	s := openTest(t)
	ctx := context.Background()
	entryReq, diag, _ := readyNetworkFixture(t, s)
	landingReq, _, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, entryReq.NodeID)
	landingNode, _ := s.GetNode(ctx, landingReq.NodeID)
	landing, _ := s.GetServer(ctx, *landingNode.ServerID)
	landing.PublicHost = host
	if err := s.UpdateServer(ctx, &landing); err != nil {
		t.Fatal(err)
	}
	diag.NetworkEgressVersion = 1
	diag.NetworkWireGuardVersion = 1
	diag.MeterInventoryVersion = 1
	if protocol == "ss2022" {
		if err := s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: corecompat.NetworkBaseline}); err != nil {
			t.Fatal(err)
		}
		diag.Cores = []agentproto.CoreStatus{{Name: "sing-box", Version: corecompat.NetworkBaseline, Installed: true}}
	}
	writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	writeNetworkDiagnostics(t, s, landing.ID, diag)
	dns := networkconfig.Resolver{Transport: "udp", Address: "203.0.113.53", Port: 53}
	in := TransitRequest{Protocol: protocol, Family: "ipv4", ID: strings.Repeat("a", 32), EntryNodeID: n.ID, LandingServerID: landing.ID, LandingAddress: landing.PublicHost, LandingPort: 51820, Outer: networkconfig.Direct{Family: "ipv4", DNS: dns}, DNS: dns}
	preview, err := s.PreviewManagedTransit(ctx, in)
	if err != nil || !preview.Ready {
		t.Fatalf("preview: %v %+v", err, preview.Checks)
	}
	in.ExpectedImpact = preview.Token
	op, err := s.CreateManagedTransit(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if protocol == "ss2022" {
		if err := s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: "1.13.21", domain.SettingSiteName: "must-not-commit"}); !errors.Is(err, ErrNetworkCoreVersion) {
			t.Fatal("SS-2022 dependency allowed incompatible downgrade", err)
		}
		if s.GetSetting(ctx, domain.SettingSiteName, "") == "must-not-commit" {
			t.Fatal("rejected downgrade partially saved settings")
		}
	}
	same, err := s.CreateManagedTransit(ctx, in, domain.AuditEvent{})
	if err != nil || same.ID != op.ID {
		t.Fatal("retry changed intent", err)
	}
	if err = s.SetManagedTransitHidden(ctx, op.ID, op.UpdatedAt, true); !errors.Is(err, ErrNetworkConflict) {
		t.Fatal("hid a transit before retirement", err)
	}
	hidden, err := s.GetNode(ctx, op.LandingNodeID)
	if !errors.Is(err, ErrNotFound) || hidden.Params != nil {
		t.Fatal("hidden endpoint exposed", err)
	}
	list, err := s.ListNodes(ctx, NodeFilter{IDs: []int64{op.LandingNodeID}, Source: domain.NodeTransit})
	if err != nil || len(list) != 0 {
		t.Fatal("internal endpoint entered node selection")
	}
	profiles, err := s.GetEgressProfile(ctx, op.ProfileID)
	if err != nil || profiles.Enabled {
		t.Fatal("entry opened before landing receipt")
	}
	if profiles.Kind != protocol || op.Protocol != protocol {
		t.Fatalf("managed transit protocol changed: profile=%q operation=%q", profiles.Kind, op.Protocol)
	}
	if err = s.DeleteServer(ctx, landing.ID); err == nil {
		t.Fatal("deleted a live landing server")
	}
	acknowledge := func(serverID int64) {
		t.Helper()
		if _, e := s.db.Exec(`INSERT INTO desired_states(server_id,revision,hash,payload,created_at) VALUES(?,COALESCE((SELECT MAX(revision)+1 FROM desired_states WHERE server_id=?),1),?,'{}',?)`, serverID, serverID, strings.Repeat("c", 64), fmtTime(s.Now())); e != nil {
			t.Fatal(e)
		}
		s.db.Exec(`UPDATE server_network_generations SET published_generation=generation WHERE server_id=?`, serverID)
		s.db.Exec(`UPDATE agents SET applied_revision=(SELECT MAX(revision) FROM desired_states WHERE server_id=?),applied_hash=?,apply_error='' WHERE server_id=?`, serverID, strings.Repeat("c", 64), serverID)
	}
	if e := s.AdvanceManagedTransit(ctx, op, "pending_entry"); !errors.Is(e, ErrNetworkConflict) {
		t.Fatal("advanced before exact receipt", e)
	}
	acknowledge(op.LandingServerID)
	if ip, e := netip.ParseAddr(host); e == nil && networkconfig.NeedsTransportGrant(ip) {
		if e = s.AdvanceManagedTransit(ctx, op, "pending_entry"); !errors.Is(e, ErrNetworkNotReady) {
			t.Fatal("private transit opened without local grant", e)
		}
		blocked, _ := s.GetEgressProfile(ctx, op.ProfileID)
		if blocked.Enabled {
			t.Fatal("private endpoint enabled before authorization")
		}
		diag.NetworkTransportVersion = 1
		diag.TransportGrants = []networkconfig.TransportGrant{{NodeID: n.ID, EgressProfileID: op.ProfileID, Purpose: protocol, Network: "udp", Address: host, Port: in.LandingPort}}
		writeNetworkDiagnostics(t, s, *n.ServerID, diag)
	}
	if err = s.AdvanceManagedTransit(ctx, op, "pending_entry"); err != nil {
		t.Fatal(err)
	}
	op, _ = s.ManagedTransit(ctx, op.ID)
	acknowledge(op.EntryServerID)
	if err = s.AdvanceManagedTransit(ctx, op, "applied"); err != nil {
		t.Fatal(err)
	}
	retirement, err := s.PreviewTransitRetirement(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RetireManagedTransit(ctx, op.ID, retirement.Token); err != nil {
		t.Fatal(err)
	}
	op, _ = s.ManagedTransit(ctx, op.ID)
	profiles, _ = s.GetEgressProfile(ctx, op.ProfileID)
	if profiles.Enabled {
		t.Fatal("retirement failed to fence entry")
	}
	var revoked bool
	s.db.QueryRow(`SELECT revoked FROM nodes WHERE id=?`, op.LandingNodeID).Scan(&revoked)
	if revoked {
		t.Fatal("landing removed before entry receipt")
	}
	acknowledge(op.EntryServerID)
	if err = s.AdvanceManagedTransit(ctx, op, "stopping_landing"); err != nil {
		t.Fatal(err)
	}
	op, _ = s.ManagedTransit(ctx, op.ID)
	acknowledge(op.LandingServerID)
	diag.RetainedNodeMeters = []int64{op.LandingNodeID}
	writeNetworkDiagnostics(t, s, landing.ID, diag)
	if err = s.AdvanceManagedTransit(ctx, op, "retired"); !errors.Is(err, ErrNetworkConflict) {
		t.Fatal("released before final accounting cleanup", err)
	}
	diag.RetainedNodeMeters = nil
	writeNetworkDiagnostics(t, s, landing.ID, diag)
	if err = s.AdvanceManagedTransit(ctx, op, "retired"); err != nil {
		t.Fatal(err)
	}
	var reservations int
	s.db.QueryRow(`SELECT count(*) FROM server_listener_reservations WHERE resource_kind='node' AND resource_id=?`, op.LandingNodeID).Scan(&reservations)
	if reservations != 0 {
		t.Fatal("retired landing port was not released")
	}
	n, _ = s.GetNode(ctx, n.ID)
	if n.Network == nil || n.Network.EgressProfileID != op.ProfileID {
		t.Fatal("retirement changed entry to direct")
	}
	meters, err := s.MeterNodes(ctx, landing.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range meters {
		if m.ID == op.LandingNodeID {
			found = m.Source == domain.NodeTransit && m.ShareID == nil
		}
	}
	if !found {
		t.Fatal("landing accounting was not isolated")
	}
	op, err = s.ManagedTransit(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetManagedTransitHidden(ctx, op.ID, op.UpdatedAt, true); err != nil {
		t.Fatal(err)
	}
	op, err = s.ManagedTransit(ctx, op.ID)
	if err != nil || !op.Hidden {
		t.Fatal("retired transit was not hidden", err)
	}
	meters, err = s.MeterNodes(ctx, landing.ID)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, m := range meters {
		if m.ID == op.LandingNodeID {
			found = m.Source == domain.NodeTransit && m.ShareID == nil
		}
	}
	if !found {
		t.Fatal("hiding a transit changed historical accounting")
	}
}
func TestInterfaceArchiveRetainsIdentity(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server := domain.Server{Name: "archive"}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	snapshot := networkFixture()
	if err := s.IngestNetwork(ctx, server.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	page, err := s.InterfaceHistory(ctx, server.ID, 0, false, 1)
	if err != nil || !page.HasMore || len(page.Items) != 1 {
		t.Fatal("pagination", err)
	}
	id := page.Items[0].ID
	if err = s.ArchiveInterface(ctx, server.ID, id, true); err == nil {
		t.Fatal("archived live interface")
	}
	snapshot.Sequence++
	original := snapshot.Interfaces
	snapshot.Interfaces = nil
	if err = s.IngestNetwork(ctx, server.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	if err = s.ArchiveInterface(ctx, server.ID, id, true); err != nil {
		t.Fatal(err)
	}
	page, err = s.InterfaceHistory(ctx, server.ID, 0, true, 20)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("missing archive", err)
	}
	snapshot.Sequence++
	snapshot.Interfaces = original
	if err = s.IngestNetwork(ctx, server.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	page, err = s.InterfaceHistory(ctx, server.ID, 0, false, 20)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("identity failed to return", err)
	}
}
