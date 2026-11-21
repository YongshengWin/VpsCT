package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func TestUnavailablePortsPersistAndRemainAdvisory(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	in, _, _ := readyNetworkFixture(t, s)
	fixtureNode, fixtureErr := s.GetNode(ctx, in.NodeID)
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	sid := *fixtureNode.ServerID
	node, err := s.GetNode(ctx, in.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	target := 34567
	before := node
	record := UnavailablePort{ServerID: sid, Port: target, Transport: "tcp", Network: " local network ", Reason: " observed failure "}
	saved, err := s.SaveUnavailablePort(ctx, record, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Network != "local network" || saved.Reason != "observed failure" {
		t.Fatal(saved)
	}
	view, err := s.ReviewNodeNetwork(ctx, node.ID, in.Network, nil, &target)
	if err != nil || !view.Ready || view.UnavailablePort == nil || view.UnavailablePort.Port != target {
		t.Fatal("manual warning incorrectly gates configuration", view, err)
	}
	other := 34568
	view, err = s.ReviewNodeNetwork(ctx, node.ID, in.Network, nil, &other)
	if err != nil || view.UnavailablePort != nil {
		t.Fatal("warning leaked to another port", view, err)
	}
	after, _ := s.GetNode(ctx, node.ID)
	if before.Port != after.Port || before.ListenPort != after.ListenPort || before.Enabled != after.Enabled {
		t.Fatal("record changed node")
	}
	later := saved.UpdatedAt.Add(time.Minute)
	s.Now = func() time.Time { return later }
	record.Transport = "both"
	record.Reason = "updated"
	saved2, err := s.SaveUnavailablePort(ctx, record, domain.AuditEvent{})
	if err != nil || !saved2.CreatedAt.Equal(saved.CreatedAt) || !saved2.UpdatedAt.After(saved.UpdatedAt) {
		t.Fatal(saved2, err)
	}
	rows, err := s.UnavailablePorts(ctx, sid)
	if err != nil || len(rows) != 1 || rows[0].Reason != "updated" {
		t.Fatal(rows, err)
	}
	if err = s.DeleteNode(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UnavailablePort(ctx, sid, target); err != nil {
		t.Fatal("deleting node erased historical port", err)
	}
	if err = s.DeleteUnavailablePort(ctx, sid, target, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteUnavailablePort(ctx, sid, target, domain.AuditEvent{}); err != nil {
		t.Fatal("delete cannot replay", err)
	}
	if _, err = s.UnavailablePort(ctx, sid, target); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestUnavailablePortAuditFailureRollsBackMutation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	fixtureNode, fixtureErr := s.GetNode(ctx, in.NodeID)
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	sid := *fixtureNode.ServerID
	record := UnavailablePort{ServerID: sid, Port: 12345, Transport: "tcp"}
	_, err := s.DB().Exec(`CREATE TRIGGER reject_port_audit BEFORE INSERT ON audit_log WHEN NEW.action LIKE 'server.port.unavailable.%' BEGIN SELECT RAISE(ABORT,'audit fixture'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveUnavailablePort(ctx, record, domain.AuditEvent{}); err == nil {
		t.Fatal("missing audit accepted")
	}
	if _, err = s.UnavailablePort(ctx, sid, record.Port); !errors.Is(err, ErrNotFound) {
		t.Fatal("un-audited record persisted", err)
	}
}
func TestProbeMigrationRetiredAndRecordsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.db")
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	in, _, _ := readyNetworkFixture(t, s)
	fixtureNode, fixtureErr := s.GetNode(ctx, in.NodeID)
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	sid := *fixtureNode.ServerID
	if _, err = s.SaveUnavailablePort(ctx, UnavailablePort{ServerID: sid, Port: 12345, Transport: "udp"}, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.UnavailablePort(ctx, sid, 12345); err != nil {
		t.Fatal(err)
	}
	var probes int
	if err = s.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('latency_tests','latency_segments')`).Scan(&probes); err != nil || probes != 0 {
		t.Fatal("obsolete probe tables remain", probes, err)
	}
}

func TestUnavailablePortCapacityAllowsUpdatesAndServerDeleteCleansRecords(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server := domain.Server{Name: "record capacity", PublicHost: "example.test", Enabled: true}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB().Exec(`WITH RECURSIVE ports(p) AS (SELECT 1 UNION ALL SELECT p+1 FROM ports WHERE p<512) INSERT INTO unavailable_ports(server_id,port,transport,created_at,updated_at) SELECT ?,p,'tcp','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z' FROM ports`, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveUnavailablePort(ctx, UnavailablePort{ServerID: server.ID, Port: 513, Transport: "tcp"}, domain.AuditEvent{}); !errors.Is(err, ErrUnavailablePortCapacity) {
		t.Fatal("capacity not enforced", err)
	}
	if _, err = s.SaveUnavailablePort(ctx, UnavailablePort{ServerID: server.ID, Port: 512, Transport: "udp", Reason: "corrected"}, domain.AuditEvent{}); err != nil {
		t.Fatal("existing record cannot be corrected at limit", err)
	}
	if err = s.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	records, err := s.UnavailablePorts(ctx, server.ID)
	if err != nil || len(records) != 0 {
		t.Fatal("server records not cleaned", len(records), err)
	}
}
