package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func forwardFixture(serverID int64, port int) domain.PortForward {
	return domain.PortForward{ServerID: serverID, Name: "fixed forward", Enabled: true, Config: networkconfig.Forward{
		ListenMode: "all", ListenPort: port, Network: "tcp", TargetHost: "origin.example.test", TargetPort: 443,
		SourceMode: "cidr", SourceCIDRs: []string{"192.0.2.0/24"}, MaxTCPConnections: 32}}
}

func TestForwardDesiredKeepsImmutableSOCKSTransport(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, _, _ := egressFixture(t, s)
	upstream := networkconfig.SOCKS5{Server: "192.0.2.2", ServerPort: 1080, Authentication: "none", Family: "ipv4", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}, Outer: networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}, ConnectTimeoutSeconds: 5}
	raw, _ := json.Marshal(upstream)
	profile := domain.EgressProfile{ServerID: server.ID, Name: "forward upstream", Kind: "socks5", Enabled: true}
	if err := s.CreateEgressProfile(ctx, &profile, raw); err != nil {
		t.Fatal(err)
	}
	f := forwardFixture(server.ID, 25104)
	f.Config.TargetHost = "203.0.113.10"
	f.Config.EgressProfileID, f.Config.EgressRevision = profile.ID, 1
	if err := s.Tx(ctx, func(tx *sql.Tx) error { var err error; f, err = s.createPortForward(ctx, tx, f); return err }); err != nil {
		t.Fatal(err)
	}
	got, err := s.DesiredForwards(ctx, server.ID)
	if err != nil || len(got) != 1 || got[0].SOCKS5 == nil || got[0].Direct != nil || got[0].SOCKS5.Config.Server != upstream.Server {
		t.Fatal("forward transport revision was lost", got, err)
	}
	if got[0].Validate() != nil || got[0].ValidateTargetSelection() != nil {
		t.Fatal("desired forward is not valid")
	}
	var egressRequired int
	if err := s.db.QueryRowContext(ctx, `SELECT egress_version FROM server_network_requirements WHERE server_id=?`, server.ID).Scan(&egressRequired); err != nil || egressRequired != 1 {
		t.Fatal("forward transport did not persist its executable requirement", err)
	}
}

func TestForwardPortsRevisionsAndOwnershipStayIndependent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, profile, node := egressFixture(t, s)
	f := forwardFixture(server.ID, node.ListenPort)
	create := func() error {
		return s.Tx(ctx, func(tx *sql.Tx) error { var err error; f, err = s.createPortForward(ctx, tx, f); return err })
	}
	if err := create(); !errors.Is(err, ErrListenPortConflict) {
		t.Fatal("node collision admitted", err)
	}
	f.Config.ListenPort = 25101
	f.Config.EgressProfileID, f.Config.EgressRevision = profile.ID, 1
	if err := create(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: "1.11.0"}); !errors.Is(err, ErrNetworkCoreVersion) {
		t.Fatal("forward pin lost version guard", err)
	}
	if version, _ := s.NetworkForwardVersion(ctx, server.ID); version != 1 {
		t.Fatal("missing cleanup contract")
	}
	if _, err := s.AppendEgressRevision(ctx, profile.ID, 1, profile.Name, true, directFixture("192.0.2.54")); err != nil {
		t.Fatal(err)
	}
	desired, err := s.DesiredForwards(ctx, server.ID)
	// Editing a template does not move this consumer's pinned version.
	if err != nil || len(desired) != 1 || desired[0].Config.EgressRevision != 1 || desired[0].Direct == nil || desired[0].Direct.DNS.Address != "192.0.2.53" {
		t.Fatal(desired, err)
	}
	if err := s.DeleteEgressProfile(ctx, profile.ID, 2); !errors.Is(err, ErrEgressInUse) {
		t.Fatal("deleted forward-owned egress", err)
	}
	before, _ := s.ListNodes(ctx, NodeFilter{ServerID: &server.ID})
	if len(before) != 1 || before[0].ID != node.ID {
		t.Fatal("forward polluted node library")
	}
	otherNode := node
	otherNode.ID, otherNode.ListenPort, otherNode.Network = 0, f.Config.ListenPort, nil
	if err := s.CreateNode(ctx, &otherNode); !errors.Is(forwardStoreError(err), ErrListenPortConflict) {
		t.Fatal("node claimed forward port", err)
	}
	old := f.Config
	f.Config.ListenPort++
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		f, err = s.updatePortForward(ctx, tx, f.ID, 1, f.Name, false, f.Config)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ports, _ := s.UsedListenPorts(ctx, server.ID)
	if !ports[old.ListenPort] || !ports[f.Config.ListenPort] {
		t.Fatal("unacknowledged old listener freed", ports)
	}
	history, err := s.PortForwardRevision(ctx, f.ID, 1)
	if err != nil || history.Config.ListenPort != old.ListenPort || !history.Enabled {
		t.Fatal(history, err)
	}
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		_, err := s.updatePortForward(ctx, tx, f.ID, 1, "stale", true, old)
		return err
	}); !errors.Is(err, ErrNetworkConflict) {
		t.Fatal("stale update accepted", err)
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error { var err error; f, err = s.retirePortForward(ctx, tx, f.ID, 2); return err })
	if err != nil || !f.Retired || f.Enabled {
		t.Fatal(f, err)
	}
	ports, _ = s.UsedListenPorts(ctx, server.ID)
	if !ports[old.ListenPort] || !ports[f.Config.ListenPort] {
		t.Fatal("retirement freed ports before cleanup")
	}
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		_, err := s.updatePortForward(ctx, tx, f.ID, 3, f.Name, true, f.Config)
		return err
	}); !errors.Is(err, ErrForwardRetired) {
		t.Fatal("retired resource resurrected", err)
	}
	if err := s.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	ports, _ = s.UsedListenPorts(ctx, server.ID)
	if len(ports) != 0 {
		t.Fatal("server deletion left reservations")
	}
}

func TestForwardFailedWritesRollbackReservationsHistoryAndGeneration(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, profile, node := egressFixture(t, s)
	f := forwardFixture(server.ID, 25101)
	err := s.Tx(ctx, func(tx *sql.Tx) error { var err error; f, err = s.createPortForward(ctx, tx, f); return err })
	if err != nil {
		t.Fatal(err)
	}
	generation, _ := s.NetworkGeneration(ctx, server.ID)
	conflict := f.Config
	conflict.ListenPort = node.ListenPort
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		_, err := s.updatePortForward(ctx, tx, f.ID, 1, "invalid", true, conflict)
		return err
	}); !errors.Is(err, ErrListenPortConflict) {
		t.Fatal(err)
	}
	if _, err := s.PortForwardRevision(ctx, f.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed edit left revision", err)
	}
	if after, _ := s.NetworkGeneration(ctx, server.ID); after != generation {
		t.Fatal("failed edit changed generation")
	}
	foreign := domain.Server{Name: "foreign"}
	if err := s.CreateServer(ctx, &foreign); err != nil {
		t.Fatal(err)
	}
	wrong := forwardFixture(foreign.ID, 25101)
	wrong.Config.EgressProfileID, wrong.Config.EgressRevision = profile.ID, 1
	if err := s.Tx(ctx, func(tx *sql.Tx) error { _, err := s.createPortForward(ctx, tx, wrong); return err }); err == nil {
		t.Fatal("cross-server egress accepted")
	}
	if forwards, _ := s.ListPortForwards(ctx, foreign.ID); len(forwards) != 0 {
		t.Fatal("failed creation left resource")
	}
}

func TestForwardOperationReceiptAuditAndReservationCommitTogether(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, _, _ := egressFixture(t, s)
	f := forwardFixture(server.ID, 25101)
	in := PortForwardRequest{ID: strings.Repeat("c", 32), Action: "create", ServerID: server.ID, Name: f.Name, Enabled: true, Config: &f.Config}
	if _, err := s.db.Exec(`CREATE TRIGGER forward_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='forward.create' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestPortForward(ctx, in, domain.AuditEvent{}); err == nil {
		t.Fatal("saved without audit")
	}
	if forwards, _ := s.ListPortForwards(ctx, server.ID); len(forwards) != 0 {
		t.Fatal("failed transaction left resource")
	}
	if ports, _ := s.UsedListenPorts(ctx, server.ID); ports[f.Config.ListenPort] {
		t.Fatal("failed transaction left port")
	}
	if _, err := s.db.Exec(`DROP TRIGGER forward_audit_failure`); err != nil {
		t.Fatal(err)
	}
	op, err := s.RequestPortForward(ctx, in, domain.AuditEvent{})
	if err != nil || op.Status != "queued" || op.Generation == 0 || op.DesiredRevision != 0 {
		t.Fatal(op, err)
	}
	if again, err := s.RequestPortForward(ctx, in, domain.AuditEvent{}); err != nil || again != op {
		t.Fatal("retry changed operation", again, err)
	}
	changed := in
	changed.Name = "changed"
	if _, err := s.RequestPortForward(ctx, changed, domain.AuditEvent{}); !errors.Is(err, ErrNetworkOperationConflict) {
		t.Fatal(err)
	}
	del := PortForwardRequest{ID: strings.Repeat("d", 32), Action: "delete", ForwardID: op.ResourceID, ExpectedRevision: 1}
	retired, err := s.RequestPortForward(ctx, del, domain.AuditEvent{})
	if err != nil || retired.Status != "queued" || retired.ResourceRevision != 2 {
		t.Fatal(retired, err)
	}
	if again, err := s.RequestPortForward(ctx, in, domain.AuditEvent{}); err != nil || again.ResourceID != op.ResourceID {
		t.Fatal("create replay resurrected resource", err)
	}
	current, _ := s.GetPortForward(ctx, op.ResourceID)
	if !current.Retired {
		t.Fatal("replayed creation resurrected resource")
	}
}

func TestForwardMigrationPreservesLegacyDuplicateOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:19] {
		if _, err := db.Exec(migration); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO schema_version(version) VALUES(19);
 INSERT INTO servers(id,name,created_at,updated_at) VALUES(1,'legacy','2026-09-21T00:00:00Z','2026-09-21T00:00:00Z');
 INSERT INTO nodes(id,name,protocol,source,server_id,listen_port,created_at,updated_at) VALUES
 (1,'old-a','ss','deployed',1,21001,'2026-09-21T00:00:00Z','2026-09-21T00:00:00Z'),
 (2,'old-b','ss','deployed',1,21001,'2026-09-21T00:00:00Z','2026-09-21T00:00:00Z');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var owners int
	if err := s.db.QueryRow(`SELECT count(*) FROM server_listener_reservations WHERE server_id=1 AND listen_port=21001`).Scan(&owners); err != nil || owners != 2 {
		t.Fatal(owners, err)
	}
	if err := s.DeleteNode(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if ports, _ := s.UsedListenPorts(ctx, 1); !ports[21001] {
		t.Fatal("one legacy owner hid other reservation")
	}
	f := forwardFixture(1, 21001)
	if err := s.Tx(ctx, func(tx *sql.Tx) error { _, err := s.createPortForward(ctx, tx, f); return err }); !errors.Is(err, ErrListenPortConflict) {
		t.Fatal("legacy port became available", err)
	}
}

func TestForwardImpactIncludesPinnedConsumersAndHistoricalListeners(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	f := forwardFixture(*n.ServerID, 25101)
	f.Config.EgressProfileID, f.Config.EgressRevision = in.Network.EgressProfileID, 1
	err := s.Tx(ctx, func(tx *sql.Tx) error { var err error; f, err = s.createPortForward(ctx, tx, f); return err })
	if err != nil {
		t.Fatal(err)
	}
	old := f.Config
	old.ListenPort--
	payload, _ := json.Marshal(agentproto.DesiredState{ServerID: f.ServerID, Forwards: []agentproto.ForwardSpec{{ForwardID: f.ID, Revision: 1, Config: old}}})
	if _, err := s.CreateDesiredState(ctx, f.ServerID, payload, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReviewNodeNetwork(ctx, n.ID, in.Network, nil)
	if err != nil || v.Impact == nil || len(v.Impact.Forwards) != 1 || !v.Impact.Forwards[0].RestartPossible || len(v.Impact.HistoricalForwards) != 1 || v.Impact.HistoricalForwards[0].ListenPort != old.ListenPort {
		t.Fatal(v, err)
	}
	profile, _ := s.GetEgressProfile(ctx, f.Config.EgressProfileID)
	view, err := s.EgressProfileView(ctx, profile.ID, 10, 0)
	if err != nil || view.ForwardReferenceCount != 1 || len(view.ForwardReferences) != 1 || view.ForwardReferences[0].Revision != 1 {
		t.Fatal(view, err)
	}
	edit := EgressProfileRequest{ID: strings.Repeat("b", 32), Action: "update", ProfileID: profile.ID, ExpectedRevision: 1, Name: profile.Name, Kind: "direct", Enabled: false, Config: directFixture("192.0.2.53")}
	preview, err := s.PreviewEgressProfile(ctx, edit)
	if err != nil || !preview.Impact.RuntimeChange || len(preview.Impact.Forwards) != 1 || preview.Impact.Forwards[0].Effect != "disable" {
		t.Fatal(preview, err)
	}
	edit.ExpectedImpact = preview.Impact.Token
	op, err := s.RequestReviewedEgressProfile(ctx, edit, domain.AuditEvent{})
	if err != nil || op.Status != "queued" {
		t.Fatal("forward runtime edit reported saved-only", op, err)
	}
}
