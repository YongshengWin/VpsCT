package traffic_test

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
	"path/filepath"
	"testing"
	"time"
)

func TestAtomicNodeAccounting(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	srv := domain.Server{Name: "fixture", PublicHost: "192.0.2.1", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	manager := share.New(st, desired.New(st))
	manager.Now = st.Now
	sh := domain.Share{Name: "fixture", QuotaBytes: 0, ResetDay: 1, Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}}
	if _, err = manager.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil {
		t.Fatal(err)
	}
	ing := traffic.New(st)
	ing.Now = st.Now
	hb := agentproto.Heartbeat{TS: now, Epoch: "boot", Ports: []agentproto.PortCounter{{NodeID: nodes[0].ID, Port: nodes[0].ListenPort, Source: "nft-node-v1", Epoch: "boot:1", FromZero: true, Rx: 100, Tx: 200}}}
	apply := func() {
		t.Helper()
		if _, err := ing.Ingest(ctx, srv, hb); err != nil {
			t.Fatal(err)
		}
	}
	assertUsage := func(rx, tx int64) {
		t.Helper()
		got, _ := st.GetShare(ctx, sh.ID)
		if got.UsedUpload != rx || got.UsedDownload != tx {
			t.Fatalf("usage %d/%d != %d/%d", got.UsedUpload, got.UsedDownload, rx, tx)
		}
		nr, nt, e := st.SumTraffic(ctx, store.SubjectNode, nodes[0].ID, now.Add(-time.Hour), now.Add(time.Hour))
		if e != nil || nr != rx || nt != tx {
			t.Fatalf("node and share disagree: %d/%d %v", nr, nt, e)
		}
	}
	apply()
	apply()
	assertUsage(100, 200)
	hb.TS = now.Add(time.Second)
	hb.Ports[0].Rx, hb.Ports[0].Tx = 150, 270
	// Abort after the counter has advanced: the entire heartbeat must roll back.
	if _, err = st.DB().Exec(`CREATE TRIGGER fail_usage BEFORE UPDATE OF used_upload ON shares BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = ing.Ingest(ctx, srv, hb); err == nil {
		t.Fatal("expected rollback")
	}
	assertUsage(100, 200)
	if _, err = st.DB().Exec(`DROP TRIGGER fail_usage`); err != nil {
		t.Fatal(err)
	}
	apply()
	assertUsage(150, 270)
	old := hb
	old.TS = now
	old.Ports = append([]agentproto.PortCounter(nil), hb.Ports...)
	old.Ports[0].Rx = 110
	if _, err = ing.Ingest(ctx, srv, old); err != nil {
		t.Fatal(err)
	}
	assertUsage(150, 270)
	hb.TS = now.Add(2 * time.Second)
	hb.Ports[0].Epoch = "boot:2"
	hb.Ports[0].Rx, hb.Ports[0].Tx = 3, 4
	apply()
	apply()
	assertUsage(153, 274)
	hb.TS = now.Add(3 * time.Second)
	hb.Ports[0].Rx = 1
	if _, err = ing.Ingest(ctx, srv, hb); err == nil {
		t.Fatal("unannounced reset accepted")
	}
	assertUsage(153, 274)
	hb.Ports[0].Rx = 4
	hb.Ports = append(hb.Ports, hb.Ports[0])
	if _, err = ing.Ingest(ctx, srv, hb); err == nil {
		t.Fatal("duplicate counter accepted")
	}
	assertUsage(153, 274)
	// A removed node still owns its unreported final bytes; a reused port must
	// never move this tail onto the new node or another share.
	hb.Ports = hb.Ports[:1]
	if err = st.DeleteNode(ctx, nodes[0].ID); err != nil {
		t.Fatal(err)
	}
	hb.Ports[0].Rx, hb.Ports[0].Tx = 9, 11
	apply()
	assertUsage(159, 281)

	// A very late retry of a prior generation must never reopen its baseline.
	replay := hb
	replay.TS = now.Add(4 * time.Second)
	replay.Ports = append([]agentproto.PortCounter(nil), hb.Ports...)
	replay.Ports[0].Epoch = "boot:1"
	replay.Ports[0].Rx, replay.Ports[0].Tx = 150, 270
	if _, err = ing.Ingest(ctx, srv, replay); err != nil {
		t.Fatal(err)
	}
	assertUsage(159, 281)

}

func TestDelayedPriorPeriodOnlyUpdatesHistory(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	srv := domain.Server{Name: "period-fixture", PublicHost: "192.0.2.1", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	manager := share.New(st, desired.New(st))
	manager.Now = st.Now
	sh := domain.Share{Name: "period-fixture", ResetDay: 1, Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}}
	if _, err = manager.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil {
		t.Fatal(err)
	}
	ing := traffic.New(st)
	ing.Now = st.Now
	ts := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	hb := agentproto.Heartbeat{TS: ts, Ports: []agentproto.PortCounter{{NodeID: nodes[0].ID, Source: "nft-node-v1", Epoch: "prior-boot", Rx: 100, Tx: 200, FromZero: true}}}
	if _, err = ing.Ingest(ctx, srv, hb); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetShare(ctx, sh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.UsedUpload != 0 || current.UsedDownload != 0 {
		t.Fatal("old period charged to current quota")
	}
	rx, tx, err := st.SumTraffic(ctx, store.SubjectShare, sh.ID, ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil || rx != 100 || tx != 200 {
		t.Fatal("old period history lost", rx, tx, err)
	}
}

func TestFinalSettlementAckIsTransactionalAndReplayable(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "final.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := domain.Server{Name: "fixture", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	node := domain.Node{Name: "fixture", ServerID: &srv.ID, Source: domain.NodeDeployed, Core: "singbox", Protocol: "vless", ListenPort: 10001}
	if err = st.CreateNode(ctx, &node); err != nil {
		t.Fatal(err)
	}
	ing := traffic.New(st)
	batch := agentproto.MeterSettlement{TS: time.Now().UTC(), Counters: []agentproto.PortCounter{{NodeID: node.ID, Source: "nft-node-v1", Epoch: "boot:generation1", FromZero: true, Rx: 100, Tx: 200}}}
	batch.ID = batch.Digest()
	hb := agentproto.Heartbeat{FinalMeters: &batch}
	// Fail precisely at ACK insertion: neither ledger nor baseline may commit.
	st.DB().Exec(`CREATE TRIGGER fail_ack BEFORE INSERT ON meter_settlements BEGIN SELECT RAISE(ABORT,'fixture failure'); END`)
	if res, err := ing.Ingest(ctx, srv, hb); err == nil || res.FinalMeterAck != "" {
		t.Fatal("uncommitted settlement acknowledged")
	}
	var count int
	st.DB().QueryRow("SELECT count(*) FROM counter_state").Scan(&count)
	if count != 0 {
		t.Fatal("baseline escaped rollback")
	}
	st.DB().Exec("DROP TRIGGER fail_ack")
	for repeat := 0; repeat < 3; repeat++ {
		res, err := ing.Ingest(ctx, srv, hb)
		if err != nil || res.FinalMeterAck != batch.ID {
			t.Fatal(err)
		}
	}
	rx, tx, err := st.SumTraffic(ctx, store.SubjectNode, node.ID, batch.TS.Add(-time.Hour), batch.TS.Add(time.Hour))
	if err != nil || rx != 100 || tx != 200 {
		t.Fatalf("duplicate final charge %d %d %v", rx, tx, err)
	}
	batch.Counters[0].Rx++
	if _, err = ing.Ingest(ctx, srv, hb); err == nil {
		t.Fatal("mutated batch ID accepted")
	}
	// A backwards wall-clock correction must not turn a final snapshot into
	// a silently ignored stale heartbeat followed by destructive ACK.
	batch.TS = batch.TS.Add(-time.Minute)
	batch.ID = batch.Digest()
	if res, err := ing.Ingest(ctx, srv, hb); err != nil || res.FinalMeterAck != batch.ID {
		t.Fatalf("clock correction: %+v %v", res, err)
	}
	rx, tx, err = st.SumTraffic(ctx, store.SubjectNode, node.ID, batch.TS.Add(-time.Hour), batch.TS.Add(time.Hour))
	if err != nil || rx != 101 || tx != 200 {
		t.Fatalf("lost final bytes: %d %d %v", rx, tx, err)
	}
	batch.Counters[0].Rx = 99
	batch.ID = batch.Digest()
	if res, err := ing.Ingest(ctx, srv, hb); err == nil || res.FinalMeterAck != "" {
		t.Fatal("conflicting final counter acknowledged")
	}
}
