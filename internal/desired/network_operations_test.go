package desired

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
)

func TestNetworkOutboxRecoversPersistedIntentAndRetryAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outbox.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	server := domain.Server{Name: "fixture", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	n, err := provision.NewNode(server, "", provision.Options{Protocol: "ss", Port: 21001})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	in := store.NodeNetworkRequest{ID: strings.Repeat("a", 32), NodeID: n.ID, Network: &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}}
	op, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	// Close after saving, before any API callback could publish anything.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A compiler/storage failure is durable and its raw text stays private.
	if _, err = s.DB().Exec(`CREATE TRIGGER fail_publish BEFORE INSERT ON desired_states BEGIN SELECT RAISE(ABORT,'fixture private detail'); END`); err != nil {
		t.Fatal(err)
	}
	if err = New(s).ReconcileNetworkOperations(ctx); err == nil || strings.Contains(err.Error(), "private detail") {
		t.Fatal("publication failure lost or exposed raw details", err)
	}
	op, err = s.NetworkOperation(ctx, op.ID)
	if err != nil || op.Status != "publish_failed" || op.Attempts != 1 || strings.Contains(op.Message, "private detail") {
		t.Fatal(op, err)
	}
	if _, err = s.DB().Exec(`DROP TRIGGER fail_publish`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return op.NextAttemptAt.Add(time.Millisecond) }
	if err = New(s).ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	op, err = s.NetworkOperation(ctx, op.ID)
	rec, recErr := s.LatestDesiredState(ctx, server.ID)
	var ds agentproto.DesiredState
	if err != nil || recErr != nil || json.Unmarshal(rec.Payload, &ds) != nil || op.Status != "waiting_agent" || op.DesiredRevision != rec.Revision || op.DesiredHash != rec.Hash {
		t.Fatal("recovered publication lost its receipt", err, recErr)
	}
	if len(ds.Nodes) != 1 || ds.Nodes[0].Network == nil || ds.Nodes[0].Network.Policy != *in.Network || ds.NetworkGeneration != op.Generation {
		t.Fatal("recovered publication lost saved policy")
	}
	if err = agentproto.ValidateDesired(&ds, server.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordNetworkApply(ctx, server.ID, rec.Revision, rec.Hash, "failed"); err != nil {
		t.Fatal(err)
	}
	// A normal deduplicated publication must not hide a failed application.
	if _, _, err = New(s).Publish(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	op, _ = s.NetworkOperation(ctx, op.ID)
	if op.Status != "apply_failed" {
		t.Fatal("dedup publication erased failure", op.Status)
	}
	if _, err = s.RetryNetworkOperation(ctx, op.ID, 0, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// The pending manual retry, including its force-publication intent, also
	// survives restart. Ordinary Publish must consume it exactly once.
	s.Now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	if err = New(s).ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	retried, _ := s.NetworkOperation(ctx, op.ID)
	if retried.Status != "waiting_agent" || retried.RetryRevision != 1 || retried.DesiredRevision <= rec.Revision || retried.DesiredHash != rec.Hash {
		t.Fatal("retry did not create a fresh receipt", retried)
	}
	if err = s.RecordNetworkApply(ctx, server.ID, rec.Revision, rec.Hash, "applied"); err != nil {
		t.Fatal(err)
	}
	still, _ := s.NetworkOperation(ctx, op.ID)
	if still.Status != "waiting_agent" {
		t.Fatal("old success satisfied a new retry")
	}
	if err = New(s).ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	latest, _, err := New(s).Publish(ctx, server.ID)
	if err != nil || latest.Revision != retried.DesiredRevision {
		t.Fatal("retry caused repeated forced publication", err)
	}
	if err = s.RecordNetworkApply(ctx, server.ID, latest.Revision, latest.Hash, "applied"); err != nil {
		t.Fatal(err)
	}
	complete, _ := s.NetworkOperation(ctx, op.ID)
	if complete.Status != "applied" {
		t.Fatal(complete.Status)
	}
}

func TestLegacyNodeDeletionQueuesCleanupAcrossControllerRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cleanup.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	server := domain.Server{Name: "fixture", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	n, err := provision.NewNode(server, "", provision.Options{Protocol: "ss", Port: 21001})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetNodeNetwork(ctx, n.ID, 0, &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}, nil); err != nil {
		t.Fatal(err)
	}
	first, _, err := New(s).Publish(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteNode(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	// No operation form was submitted and no post-delete callback ran.
	if operations, err := s.NetworkOperations(ctx, server.ID, 100, 0); err != nil || len(operations) != 0 {
		t.Fatal("fixture unexpectedly used a network operation", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = New(s).ReconcileNetworkOperations(ctx); err != nil {
		t.Fatal(err)
	}
	cleanup, err := s.LatestDesiredState(ctx, server.ID)
	var ds agentproto.DesiredState
	if err != nil || json.Unmarshal(cleanup.Payload, &ds) != nil || cleanup.Revision <= first.Revision || len(ds.Nodes) != 0 || ds.NetworkBindingVersion != 1 {
		t.Fatal("deletion failed to publish versioned cleanup after restart", err)
	}
	if pending, err := s.PendingNetworkPublications(ctx); err != nil || len(pending) != 0 {
		t.Fatal("cleanup publication was not acknowledged atomically", err)
	}
}
