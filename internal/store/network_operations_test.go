package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

func operationFixture(t *testing.T, s *Store) NodeNetworkRequest {
	t.Helper()
	_, p, n := egressFixture(t, s)
	return NodeNetworkRequest{ID: strings.Repeat("a", 32), NodeID: n.ID, Network: nodePolicy(p)}
}

func TestNetworkRequestIsAtomicAuditedAndIdempotent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := operationFixture(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='node.network.request' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{}); err == nil {
		t.Fatal("request survived audit failure")
	}
	n, err := s.GetNode(ctx, in.NodeID)
	if err != nil || n.Network != nil || n.NetworkRevision != 0 {
		t.Fatal("failed transaction changed the node", err)
	}
	if gen, err := s.NetworkGeneration(ctx, *n.ServerID); err != nil || gen != 0 {
		t.Fatal("failed transaction advanced generation", gen, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_audit_failure`); err != nil {
		t.Fatal(err)
	}
	op, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil || op.Status != "queued" || op.ResourceRevision != 1 || op.Generation != 1 || op.DesiredRevision != 0 {
		t.Fatal(op, err)
	}
	retry, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil || retry != op {
		t.Fatal("exact retry created another operation", retry, err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='node.network.request'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("retry duplicated audit", count, err)
	}
	changed := in
	changed.ExpectedRevision++
	if _, err := s.RequestNodeNetwork(ctx, changed, domain.AuditEvent{}); !errors.Is(err, ErrNetworkOperationConflict) {
		t.Fatal("operation key reused with different request", err)
	}
	wire, _ := json.Marshal(op)
	if strings.Contains(string(wire), op.RequestHash) || strings.Contains(string(wire), "desired_hash") {
		t.Fatal("internal receipt digest exposed")
	}
	if err := s.DeleteNode(ctx, in.NodeID); err != nil {
		t.Fatal(err)
	}
	if retry, err := s.ExistingNodeNetworkRequest(ctx, in); err != nil || retry.Status != "superseded" {
		t.Fatal("deleted resource broke idempotency or kept pending intent", err)
	}
}

func operationCandidate(op NetworkOperation) *agentproto.DesiredState {
	ds := &agentproto.DesiredState{ServerID: op.ServerID, NetworkBindingVersion: 1, NetworkGeneration: op.Generation}
	ds.Hash = agentproto.ContentHash(ds)
	return ds
}

func TestNetworkPublicationAndExactReceiptsShareOneTransaction(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	op, err := s.RequestNodeNetwork(ctx, operationFixture(t, s), domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	ds := operationCandidate(op)
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_receipt_failure BEFORE UPDATE ON network_operations WHEN NEW.status='waiting_agent' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PublishDesired(ctx, 0, ds, false); err == nil {
		t.Fatal("publication survived missing receipt")
	}
	if _, err := s.LatestDesiredState(ctx, op.ServerID); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial desired publication", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_receipt_failure`); err != nil {
		t.Fatal(err)
	}
	rec, _, err := s.PublishDesired(ctx, 0, ds, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []struct {
		server, revision int64
		hash, status     string
	}{{op.ServerID, rec.Revision, strings.Repeat("f", 64), "applied"}, {op.ServerID, rec.Revision + 1, rec.Hash, "applied"}, {op.ServerID + 1, rec.Revision, rec.Hash, "applied"}, {op.ServerID, rec.Revision, rec.Hash, "pending"}} {
		if err := s.RecordNetworkApply(ctx, report.server, report.revision, report.hash, report.status); err != nil {
			t.Fatal(err)
		}
		got, _ := s.NetworkOperation(ctx, op.ID)
		if got.Status != "waiting_agent" {
			t.Fatal("unmatched report completed operation", got)
		}
	}
	if err := s.RecordNetworkApply(ctx, op.ServerID, rec.Revision, rec.Hash, "failed"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.NetworkOperation(ctx, op.ID)
	if got.Status != "apply_failed" {
		t.Fatal(got)
	}
	if err := s.RecordNetworkApply(ctx, op.ServerID, rec.Revision, rec.Hash, "applied"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.NetworkOperation(ctx, op.ID)
	if got.Status != "applied" {
		t.Fatal(got)
	}
}

func TestNewNetworkIntentRejectsUnpublishedStaleBuildAndLateReceipt(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := operationFixture(t, s)
	op, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	old := operationCandidate(op)
	rec, _, err := s.PublishDesired(ctx, 0, old, false)
	if err != nil {
		t.Fatal(err)
	}
	in.ID, in.ExpectedRevision, in.Network = strings.Repeat("b", 32), 1, nil
	next, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil || next.Generation <= op.Generation {
		t.Fatal(next, err)
	}
	// No newer desired row exists yet: comparing desired revision alone used
	// to miss the newer saved intent in this exact save/publish gap.
	if _, _, err := s.PublishDesired(ctx, rec.Revision, old, true); !errors.Is(err, ErrDesiredConflict) {
		t.Fatal("stale build published over newer unpublished intent", err)
	}
	if err := s.RecordNetworkApply(ctx, op.ServerID, rec.Revision, rec.Hash, "applied"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.NetworkOperation(ctx, op.ID)
	if got.Status != "superseded" {
		t.Fatal("late report revived old operation", got)
	}
}

func TestNetworkPublicationRetriesAreBounded(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	op, err := s.RequestNodeNetwork(ctx, operationFixture(t, s), domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	// A whole-second deadline sorts before a later fractional timestamp.
	now = now.Add(time.Millisecond)
	for i := 0; i < MaxNetworkPublishAttempts; i++ {
		pending, err := s.PendingNetworkPublications(ctx)
		if err != nil || len(pending) != 1 {
			t.Fatal("lost retry", pending, err)
		}
		attempt := pending[0]
		if err = s.NetworkPublicationFailed(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		// A duplicate worker's result cannot increment attempts twice.
		if err = s.NetworkPublicationFailed(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		op, err = s.NetworkOperation(ctx, op.ID)
		if err != nil || op.Attempts != i+1 || !op.NextAttemptAt.After(now) || op.NextAttemptAt.Sub(now) > time.Minute {
			t.Fatal("unbounded retry", op, err)
		}
		now = op.NextAttemptAt.Add(time.Millisecond)
	}
	pending, err := s.PendingNetworkPublications(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatal("automatic retry budget ignored", err)
	}
}

func TestNetworkRetryIsAuditedCASAndRejectsPreviousWorker(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := operationFixture(t, s)
	op, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryNetworkOperation(ctx, op.ID, 0, domain.AuditEvent{}); !errors.Is(err, ErrNetworkRetryConflict) {
		t.Fatal("already queued operation retried", err)
	}
	stale := NetworkPublication{ServerID: op.ServerID, Generation: op.Generation}
	if err = s.NetworkPublicationFailed(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fixture_retry_audit BEFORE INSERT ON audit_log WHEN NEW.action='network.operation.retry' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryNetworkOperation(ctx, op.ID, 0, domain.AuditEvent{}); err == nil {
		t.Fatal("retry survived audit failure")
	}
	still, _ := s.NetworkOperation(ctx, op.ID)
	if still.Status != "publish_failed" || still.RetryRevision != 0 || still.Attempts != 1 {
		t.Fatal("audit failure partially retried", still)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fixture_retry_audit`); err != nil {
		t.Fatal(err)
	}
	retry, err := s.RetryNetworkOperation(ctx, op.ID, 0, domain.AuditEvent{})
	if err != nil || retry.RetryRevision != 1 || retry.Attempts != 0 || retry.Status != "queued" {
		t.Fatal(retry, err)
	}
	if _, err = s.RetryNetworkOperation(ctx, op.ID, 0, domain.AuditEvent{}); !errors.Is(err, ErrNetworkRetryConflict) {
		t.Fatal("duplicate retry reset budget twice", err)
	}
	// The old worker had attempts=0 too; the retry revision must distinguish it.
	if err = s.NetworkPublicationFailed(ctx, stale); err != nil {
		t.Fatal(err)
	}
	still, _ = s.NetworkOperation(ctx, op.ID)
	if still.Status != "queued" || still.Attempts != 0 {
		t.Fatal("old worker consumed fresh retry budget", still)
	}
	if _, err = s.SetNodeNetwork(ctx, in.NodeID, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryNetworkOperation(ctx, op.ID, 1, domain.AuditEvent{}); !errors.Is(err, ErrNetworkRetryConflict) {
		t.Fatal("superseded intent retried", err)
	}
}

func TestConcurrentNetworkRequestCreatesOneIntent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := operationFixture(t, s)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrNetworkConflict) {
			t.Fatal(err)
		}
	}
	op, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil || op.ResourceRevision != 1 || op.Generation != 1 {
		t.Fatal("concurrent duplicate changed policy twice", op, err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='node.network.request'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestNetworkGenerationCoversProfileStateAndServerCleanup(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, p, n := egressFixture(t, s)
	op, err := s.RequestNodeNetwork(ctx, NodeNetworkRequest{ID: strings.Repeat("a", 32), NodeID: n.ID, Network: nodePolicy(p)}, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	// New immutable content is not followed automatically by existing bindings.
	if _, err = s.AppendEgressRevision(ctx, p.ID, 1, "new content", true, directFixture("192.0.2.54")); err != nil {
		t.Fatal(err)
	}
	gen, _ := s.NetworkGeneration(ctx, server.ID)
	if gen != op.Generation {
		t.Fatal("unbound revision changed active generation")
	}
	if _, err = s.AppendEgressRevision(ctx, p.ID, 2, "disabled", false, directFixture("192.0.2.54")); err != nil {
		t.Fatal(err)
	}
	gen, _ = s.NetworkGeneration(ctx, server.ID)
	old, _ := s.NetworkOperation(ctx, op.ID)
	if gen <= op.Generation || old.Status != "superseded" {
		t.Fatal("profile disable did not supersede pending intent", gen, old.Status)
	}
	if err = s.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if gen, err = s.NetworkGeneration(ctx, server.ID); err != nil || gen != 0 {
		t.Fatal("generation delete trigger recreated deleted server", gen, err)
	}
	if _, err = s.NetworkOperation(ctx, op.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("server deletion left an orphan operation", err)
	}
}

func TestNetworkGenerationBracketsLegacyDesiredInputsWithoutHeartbeatChurn(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := operationFixture(t, s)
	op, err := s.RequestNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	var current int64
	assertChanged := func(query string, args ...any) {
		t.Helper()
		previous, _ := s.NetworkGeneration(ctx, op.ServerID)
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
		current, err = s.NetworkGeneration(ctx, op.ServerID)
		if err != nil || current <= previous {
			t.Fatal("saved desired input did not invalidate old candidate", query, err)
		}
	}
	assertChanged(`UPDATE nodes SET server_params='{}' WHERE id=?`, in.NodeID)
	assertChanged(`UPDATE nodes SET enabled=0 WHERE id=?`, in.NodeID)
	assertChanged(`UPDATE nodes SET revoked=1 WHERE id=?`, in.NodeID)
	assertChanged(`UPDATE servers SET ipv4_only=1 WHERE id=?`, op.ServerID)
	assertChanged(`INSERT INTO settings(key,value) VALUES('connlog.self_enabled','false')`)
	assertChanged(`UPDATE settings SET value='true' WHERE key='connlog.self_enabled'`)
	assertChanged(`DELETE FROM settings WHERE key='connlog.self_enabled'`)
	if _, err = s.db.Exec(`INSERT INTO shares(id,name,status,period_start,created_at,updated_at) VALUES(100,'fixture','active','','','')`); err != nil {
		t.Fatal(err)
	}
	assertChanged(`UPDATE nodes SET share_id=100 WHERE id=?`, in.NodeID)
	assertChanged(`UPDATE shares SET status='paused' WHERE id=100`)
	assertChanged(`UPDATE shares SET connlog_enabled=1 WHERE id=100`)
	for _, query := range []string{
		`UPDATE shares SET used_upload=used_upload+1 WHERE id=100`,
		`UPDATE nodes SET server='observation.example.test',updated_at='observation'`,
		`UPDATE servers SET notes='observation',quota_bytes=1000`,
		`INSERT INTO settings(key,value) VALUES('unrelated.fixture','value')`,
	} {
		if _, err = s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
		if got, err := s.NetworkGeneration(ctx, op.ServerID); err != nil || got != current {
			t.Fatal("non-runtime observation invalidated configured service", query, err)
		}
	}
	if _, _, err := s.PublishDesired(ctx, 0, operationCandidate(op), false); !errors.Is(err, ErrDesiredConflict) {
		t.Fatal("old candidate restored revoked credentials", err)
	}
	// Servers that never opted in retain their legacy wire/hash contract.
	legacy, _, _ := egressFixture(t, s)
	if _, err = s.db.Exec(`UPDATE servers SET ipv4_only=1 WHERE id=?`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if gen, err := s.NetworkGeneration(ctx, legacy.ID); err != nil || gen != 0 {
		t.Fatal("ordinary legacy update opted a server into networking", gen, err)
	}
}
