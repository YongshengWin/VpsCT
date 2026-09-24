package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/maintenance"
	"ctlvps/internal/networkconfig"
)

func TestSS2022TemplateKeepsEncryptedKeyAcrossRevisions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server := domain.Server{Name: "SS-2022 template"}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(networkconfig.SS2022{Server: "198.51.100.2", ServerPort: 8388, Method: "2022-blake3-aes-128-gcm", Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.53", Port: 53}, Outer: networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.53", Port: 53}}, ConnectTimeoutSeconds: 10})
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 16))
	in := EgressProfileRequest{ID: strings.Repeat("9", 32), Action: "create", ServerID: server.ID, Name: "private upstream", Kind: "ss2022", Enabled: true, Config: config}
	if _, err := s.RequestEgressProfile(ctx, in, domain.AuditEvent{}); err == nil {
		t.Fatal("accepted SS-2022 without key")
	}
	in.Credentials = &networkconfig.SOCKS5Credentials{Password: key}
	op, err := s.RequestEgressProfile(ctx, in, domain.AuditEvent{})
	if err != nil || op.Status != "saved" {
		t.Fatal(op, err)
	}
	var sealed string
	if err := s.db.QueryRow(`SELECT credentials FROM egress_profile_credentials WHERE profile_id=? AND revision=1`, op.ResourceID).Scan(&sealed); err != nil || !strings.HasPrefix(sealed, secretPrefix) || strings.Contains(sealed, key) {
		t.Fatal("key was not sealed", err)
	}
	in = EgressProfileRequest{ID: strings.Repeat("8", 32), Action: "update", ProfileID: op.ResourceID, ExpectedRevision: 1, Name: "private upstream", Kind: "ss2022", Enabled: true, Config: config}
	if _, err := s.RequestEgressProfile(ctx, in, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	retained, err := s.egressCredentials(ctx, s.db, server.ID, op.ResourceID, 2)
	if err != nil || retained.Password != key {
		t.Fatal("updated revision lost key", err)
	}
}

func TestEgressManagementReceiptsAndAuditAreAtomic(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server := domain.Server{Name: "egress management"}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	in := EgressProfileRequest{ID: strings.Repeat("1", 32), Action: "create", ServerID: server.ID, Name: "direct", Kind: "direct", Enabled: true, Config: directFixture("192.0.2.53")}
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_egress_audit BEFORE INSERT ON audit_log WHEN NEW.action='egress.create' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestEgressProfile(ctx, in, domain.AuditEvent{}); err == nil {
		t.Fatal("saved without audit")
	}
	profiles, _ := s.ListEgressProfiles(ctx, server.ID)
	if len(profiles) != 0 {
		t.Fatal("audit failure left profile")
	}
	if _, err := s.NetworkOperation(ctx, in.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("audit failure left receipt", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_egress_audit`); err != nil {
		t.Fatal(err)
	}
	op, err := s.RequestEgressProfile(ctx, in, domain.AuditEvent{})
	if err != nil || op.Status != "saved" || op.Generation != 0 || op.DesiredRevision != 0 {
		t.Fatal(op, err)
	}
	if generation, _ := s.NetworkGeneration(ctx, server.ID); generation != 0 {
		t.Fatal("unused template opted server into runtime")
	}
	if replay, err := s.RequestEgressProfile(ctx, in, domain.AuditEvent{}); err != nil || replay != op {
		t.Fatal("create not idempotent", replay, err)
	}
	changed := in
	changed.Name = "other"
	if _, err := s.RequestEgressProfile(ctx, changed, domain.AuditEvent{}); !errors.Is(err, ErrNetworkOperationConflict) {
		t.Fatal("key accepted changed content", err)
	}
	if _, err := s.RetryNetworkOperation(ctx, op.ID, 0, domain.AuditEvent{}); !errors.Is(err, ErrNetworkRetryConflict) {
		t.Fatal("template incorrectly scheduled for agent", err)
	}
	del := EgressProfileRequest{ID: strings.Repeat("2", 32), Action: "delete", ProfileID: op.ResourceID, ExpectedRevision: 1}
	deleted, err := s.RequestEgressProfile(ctx, del, domain.AuditEvent{})
	if err != nil || deleted.Status != "saved" {
		t.Fatal(deleted, err)
	}
	if replay, err := s.RequestEgressProfile(ctx, del, domain.AuditEvent{}); err != nil || replay != deleted {
		t.Fatal("delete replay lost receipt", err)
	}
	if replay, err := s.RequestEgressProfile(ctx, in, domain.AuditEvent{}); err != nil || replay != op {
		t.Fatal("create replay resurrected deleted resource", err)
	}
	profiles, _ = s.ListEgressProfiles(ctx, server.ID)
	if len(profiles) != 0 {
		t.Fatal("deleted template recreated")
	}
	var audits int
	if err := s.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action LIKE 'egress.%'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatal(audits, err)
	}
	wire, _ := json.Marshal(op)
	if strings.Contains(string(wire), op.RequestHash) {
		t.Fatal("request digest leaked")
	}
}

func TestEgressEnableChecksPinnedVersionsAndDisableQueuesWhileOffline(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	request, diag, snapshot := readyNetworkFixture(t, s)
	if _, err := s.RequestReadyNodeNetwork(ctx, request, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetEgressProfile(ctx, request.Network.EgressProfileID)
	edit := EgressProfileRequest{ID: strings.Repeat("2", 32), Action: "update", ProfileID: p.ID, ExpectedRevision: 1, Name: p.Name, Kind: "direct", Enabled: true, Config: directFixture("192.0.2.54")}
	before, _ := s.NetworkGeneration(ctx, p.ServerID)
	op, err := s.RequestEgressProfile(ctx, edit, domain.AuditEvent{})
	if err != nil || op.Status != "saved" || op.ResourceRevision != 2 {
		t.Fatal(op, err)
	}
	if after, _ := s.NetworkGeneration(ctx, p.ServerID); after != before {
		t.Fatal("new default version changed pinned node")
	}
	view, err := s.EgressProfileView(ctx, p.ID, 1, 0)
	if err != nil || view.ReferenceCount != 1 || view.References[0].Revision != 1 || view.Profile.CurrentRevision != 2 {
		t.Fatal(view, err)
	}
	if _, err := s.RequestEgressProfile(ctx, EgressProfileRequest{ID: strings.Repeat("3", 32), Action: "delete", ProfileID: p.ID, ExpectedRevision: 2}, domain.AuditEvent{}); !errors.Is(err, ErrEgressInUse) {
		t.Fatal("deleted used profile", err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET last_seen_at=NULL WHERE server_id=?`, p.ServerID); err != nil {
		t.Fatal(err)
	}
	edit.ID, edit.ExpectedRevision, edit.Enabled = strings.Repeat("4", 32), 2, false
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_egress_audit BEFORE INSERT ON audit_log WHEN NEW.action='egress.update' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestEgressProfile(ctx, edit, domain.AuditEvent{}); err == nil {
		t.Fatal("runtime change committed without audit")
	}
	failedProfile, _ := s.GetEgressProfile(ctx, p.ID)
	if !failedProfile.Enabled || failedProfile.CurrentRevision != 2 {
		t.Fatal("failed audit changed live profile state")
	}
	if generation, _ := s.NetworkGeneration(ctx, p.ServerID); generation != before {
		t.Fatal("failed audit advanced publication generation")
	}
	if _, err := s.NetworkOperation(ctx, edit.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed audit left runtime receipt", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_egress_audit`); err != nil {
		t.Fatal(err)
	}
	deactivated, err := s.RequestEgressProfile(ctx, edit, domain.AuditEvent{})
	if err != nil || deactivated.Status != "queued" || deactivated.Generation <= before {
		t.Fatal(deactivated, err)
	}
	// A newer saved template remains terminal when later runtime edits happen.
	old, _ := s.NetworkOperation(ctx, op.ID)
	if old.Status != "saved" {
		t.Fatal("metadata receipt superseded", old)
	}
	// Give the *new* default a replacement NIC. Existing node still pins v1.
	edit.ID, edit.ExpectedRevision, edit.Enabled = strings.Repeat("5", 32), 3, true
	edit.Config = json.RawMessage(strings.ReplaceAll(string(edit.Config), strings.Repeat("1", 32), strings.Repeat("2", 32)))
	writeNetworkDiagnostics(t, s, p.ServerID, diag)
	snapshot.Sequence++
	snapshot.Interfaces[0].ID = strings.Repeat("2", 32)
	if err := s.IngestNetwork(ctx, p.ServerID, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestEgressProfile(ctx, edit, domain.AuditEvent{}); !errors.Is(err, ErrNetworkNotReady) {
		t.Fatal("latest config hid invalid pinned binding", err)
	}
	p, _ = s.GetEgressProfile(ctx, p.ID)
	if p.Enabled || p.CurrentRevision != 3 {
		t.Fatal("failed enable partially changed profile", p)
	}
	if generation, _ := s.NetworkGeneration(ctx, p.ServerID); generation != deactivated.Generation {
		t.Fatal("failed enable advanced outbox")
	}
	// Repair the old identity: enabling uses its original version, not v4.
	snapshot.Sequence++
	snapshot.Interfaces[0].ID = strings.Repeat("1", 32)
	if err := s.IngestNetwork(ctx, p.ServerID, snapshot); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.RequestEgressProfile(ctx, edit, domain.AuditEvent{})
	if err != nil || enabled.Status != "queued" || enabled.ResourceRevision != 4 {
		t.Fatal(enabled, err)
	}
	bound, _ := s.GetNode(ctx, request.NodeID)
	if bound.Network.EgressRevision != 1 {
		t.Fatal("enable upgraded pinned revision")
	}
	job := MaintenanceJob{ServerID: p.ServerID, Job: maintenance.Job{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "uninstall"}, Status: "queued", CreatedAt: s.Now(), UpdatedAt: s.Now()}}
	if err := s.CreateMaintenance(ctx, job); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.RequestEgressProfile(ctx, edit, domain.AuditEvent{}); err != nil || replay.ID != enabled.ID {
		t.Fatal("maintenance broke exact retry", err)
	}
	edit.ID, edit.ExpectedRevision = strings.Repeat("6", 32), 4
	if _, err := s.RequestEgressProfile(ctx, edit, domain.AuditEvent{}); !errors.Is(err, ErrNetworkMaintenance) {
		t.Fatal("edit bypassed maintenance", err)
	}
}

func TestEgressOperationMigrationPreservesPendingReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v16.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:16] {
		if _, err := db.Exec(migration); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO schema_version(version) VALUES(16);
 INSERT INTO servers(id,name,created_at,updated_at) VALUES(1,'fixture','2026-09-20T00:00:00Z','2026-09-20T00:00:00Z');
 INSERT INTO server_network_generations(server_id,generation) VALUES(1,2);
 INSERT INTO network_operations(id,server_id,kind,resource_id,resource_revision,generation,request_hash,status,desired_revision,desired_hash,retry_revision,republish,next_attempt_at,message,created_at,updated_at)
 VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'node_network',1,1,2,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','waiting_agent',9,'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',3,1,'2026-09-20T00:00:00Z','fixture','2026-09-20T00:00:00Z','2026-09-20T00:00:00Z');`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	op, err := s.NetworkOperation(context.Background(), strings.Repeat("a", 32))
	if err != nil || op.Status != "waiting_agent" || op.DesiredRevision != 9 || op.RetryRevision != 3 || op.DesiredHash != strings.Repeat("c", 64) {
		t.Fatal(op, err)
	}
	var republish int
	if err := s.db.QueryRow(`SELECT republish FROM network_operations WHERE id=?`, op.ID).Scan(&republish); err != nil || republish != 1 {
		t.Fatal(republish, err)
	}
	if _, err := s.db.Exec(`UPDATE server_network_generations SET generation=3 WHERE server_id=1`); err != nil {
		t.Fatal(err)
	}
	op, err = s.NetworkOperation(context.Background(), op.ID)
	if err != nil || op.Status != "superseded" {
		t.Fatal("migration lost invalidation trigger", op, err)
	}
}
