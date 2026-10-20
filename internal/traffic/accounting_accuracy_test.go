package traffic_test

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

func accurateLedgerFixture(t *testing.T, count, resetDay int) (*store.Store, *traffic.Ingestor, domain.Server, domain.Share, []domain.Node, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "accuracy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	server := domain.Server{Name: "accuracy fixture", Enabled: true}
	if err := st.CreateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	sh := domain.Share{Name: "accuracy fixture", ResetDay: resetDay, PeriodStart: now.Add(-24 * time.Hour)}
	if err := st.CreateShare(context.Background(), &sh); err != nil {
		t.Fatal(err)
	}
	var nodes []domain.Node
	for index := 0; index < count; index++ {
		n := domain.Node{Name: fmt.Sprint("node fixture ", index), Source: domain.NodeDeployed, ServerID: &server.ID, Core: domain.CoreSingBox, Protocol: "ss", ListenPort: 21000 + index, ShareID: &sh.ID, Enabled: true}
		if err := st.CreateNode(context.Background(), &n); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	ing := traffic.New(st)
	ing.Now = st.Now
	return st, ing, server, sh, nodes, &now
}

func requireAccurateTotals(t *testing.T, st *store.Store, subject string, id int64, at time.Time, inbound, outbound int64) {
	t.Helper()
	in, out, err := st.SumTraffic(context.Background(), subject, id, at.Add(-24*time.Hour), at.Add(24*time.Hour))
	if err != nil || in != inbound || out != outbound {
		t.Fatalf("%s totals=%d/%d want=%d/%d err=%v", subject, in, out, inbound, outbound, err)
	}
}

func TestLedgerDuplicateTimestampAndIndependentLegacyDirectionReset(t *testing.T) {
	st, ing, server, sh, nodes, now := accurateLedgerFixture(t, 1, 0)
	ctx := context.Background()
	hb := agentproto.Heartbeat{TS: *now, Epoch: "legacy", Metrics: agentproto.Metrics{NetRx: 1000, NetTx: 2000}, Ports: []agentproto.PortCounter{{Port: nodes[0].ListenPort, Rx: 100, Tx: 200}}}
	for repeat := 0; repeat < 2; repeat++ {
		if _, err := ing.Ingest(ctx, server, hb); err != nil {
			t.Fatal(err)
		}
	}
	for _, counters := range [][2]int64{{900, 2000}} {
		conflict := hb
		conflict.Metrics.NetRx, conflict.Metrics.NetTx = counters[0], counters[1]
		if _, err := ing.Ingest(ctx, server, conflict); err == nil {
			t.Fatal("same timestamp changed NIC counters")
		}
	}
	conflict := hb
	conflict.Ports = append([]agentproto.PortCounter(nil), hb.Ports...)
	conflict.Ports[0].Rx--
	if _, err := ing.Ingest(ctx, server, conflict); err == nil {
		t.Fatal("same timestamp changed node counters")
	}
	for _, nodeID := range []*int64{nil, &nodes[0].ID} {
		samples, err := st.ListSamples(ctx, server.ID, nodeID, now.Add(-time.Hour))
		if err != nil || len(samples) != 1 {
			t.Fatal("duplicate or conflicting reports changed raw history", len(samples), err)
		}
	}
	hb.TS = hb.TS.Add(time.Second)
	hb.Metrics.NetRx, hb.Metrics.NetTx = 10, 2100
	hb.Ports[0].Rx, hb.Ports[0].Tx = 101, 205
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	hb.TS = hb.TS.Add(time.Second)
	hb.Metrics.NetRx, hb.Metrics.NetTx = 20, 3
	hb.Ports[0].Rx, hb.Ports[0].Tx = 2, 210
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	stale := hb
	stale.TS = *now
	stale.Metrics.NetRx, stale.Metrics.NetTx = 9000, 9000
	if _, err := ing.Ingest(ctx, server, stale); err != nil {
		t.Fatal(err)
	}
	requireAccurateTotals(t, st, store.SubjectServer, server.ID, *now, 20, 103)
	requireAccurateTotals(t, st, store.SubjectNode, nodes[0].ID, *now, 3, 10)
	requireAccurateTotals(t, st, store.SubjectShare, sh.ID, *now, 3, 10)
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.UsedUpload != 3 || got.UsedDownload != 10 {
		t.Fatal("legacy direction reset overcharged share", err)
	}
}

func TestLegacyCoarseOrMissingTimestampAcceptsNewCountersAndDeduplicatesReplay(t *testing.T) {
	for _, identified := range []bool{false, true} {
		t.Run(fmt.Sprint("identified=", identified), func(t *testing.T) {
			st, ing, server, sh, nodes, now := accurateLedgerFixture(t, 1, 0)
			ctx := context.Background()
			// Ordinary heartbeat timestamps are not immutable sample IDs. A
			// coarse agent or fixed receiver clock may see two monotonic readings.
			hb := agentproto.Heartbeat{Epoch: "legacy", Metrics: agentproto.Metrics{NetRx: 100, NetTx: 200}, Ports: []agentproto.PortCounter{{Port: nodes[0].ListenPort, Rx: 10, Tx: 20}}}
			if identified {
				hb.TS = *now
			}
			if _, err := ing.Ingest(ctx, server, hb); err != nil {
				t.Fatal(err)
			}
			hb.Metrics.NetRx, hb.Metrics.NetTx = 150, 270
			hb.Ports[0].Rx, hb.Ports[0].Tx = 20, 40
			for repeat := 0; repeat < 2; repeat++ {
				if _, err := ing.Ingest(ctx, server, hb); err != nil {
					t.Fatal(err)
				}
			}
			requireAccurateTotals(t, st, store.SubjectServer, server.ID, *now, 50, 70)
			requireAccurateTotals(t, st, store.SubjectShare, sh.ID, *now, 10, 20)
			for _, nodeID := range []*int64{nil, &nodes[0].ID} {
				samples, err := st.ListSamples(ctx, server.ID, nodeID, now.Add(-time.Hour))
				if err != nil || len(samples) != 2 {
					t.Fatal("coarse timestamps lost new data or duplicated a replay", len(samples), err)
				}
			}
		})
	}
}

func TestNICRealBootResetAndLegacyRestartCompatibility(t *testing.T) {
	st, ing, server, _, _, now := accurateLedgerFixture(t, 0, 0)
	ctx := context.Background()
	bootA := "11111111-1111-4111-8111-111111111111"
	bootB := "22222222-2222-4222-8222-222222222222"
	hb := agentproto.Heartbeat{TS: *now, Epoch: bootA + ":nonce1", Metrics: agentproto.Metrics{NetRx: 100, NetTx: 200}}
	ingest := func() {
		t.Helper()
		if _, err := ing.Ingest(ctx, server, hb); err != nil {
			t.Fatal(err)
		}
	}
	ingest()
	hb.TS = hb.TS.Add(time.Second)
	hb.Epoch, hb.Metrics.NetRx, hb.Metrics.NetTx = bootA+":nonce2", 150, 270
	ingest() // An nft nonce does not reset the physical NIC.
	hb.TS = hb.TS.Add(time.Second)
	hb.Epoch, hb.Metrics.NetRx, hb.Metrics.NetTx = bootB+":nonce3", 180, 300
	ingest() // The new boot's first reading may exceed the old boot's reading.
	hb.TS = hb.TS.Add(time.Second)
	hb.Epoch, hb.Metrics.NetRx, hb.Metrics.NetTx = bootA+":nonce2", 800, 900
	ingest() // A delayed retired boot must not replace the current NIC baseline.
	hb.TS = hb.TS.Add(time.Second)
	hb.Epoch, hb.Metrics.NetRx, hb.Metrics.NetTx = bootB+":nonce4", 190, 305
	ingest()
	requireAccurateTotals(t, st, store.SubjectServer, server.ID, *now, 240, 375)
	hb.TS = hb.TS.Add(time.Second)
	hb.Epoch, hb.Metrics.NetRx, hb.Metrics.NetTx = "unknown-agent-restart", 500, 600
	ingest()
	hb.TS = hb.TS.Add(time.Second)
	hb.Epoch, hb.Metrics.NetRx, hb.Metrics.NetTx = "another-unknown-restart", 550, 620
	ingest()
	requireAccurateTotals(t, st, store.SubjectServer, server.ID, *now, 240, 375)
	samples, err := st.ListSamples(ctx, server.ID, nil, now.Add(-time.Hour))
	if err != nil || len(samples) != 6 {
		t.Fatal("retired NIC boot entered raw chart history", len(samples), err)
	}
}

func TestNodeMeterOverlapRejectedButFrozenMigrationCountsOnce(t *testing.T) {
	st, ing, server, sh, nodes, now := accurateLedgerFixture(t, 1, 0)
	ctx := context.Background()
	legacy := agentproto.PortCounter{Port: nodes[0].ListenPort, Rx: 100, Tx: 200}
	identified := legacy
	identified.NodeID, identified.Source, identified.Epoch = nodes[0].ID, "systemd-legacy-v1", "legacy-service"
	hb := agentproto.Heartbeat{TS: *now, Epoch: "legacy", Ports: []agentproto.PortCounter{legacy}}
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	hb.Ports = []agentproto.PortCounter{identified}
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	legacy.Rx, legacy.Tx = 150, 250
	identified.Rx, identified.Tx = 150, 250
	hb.TS = hb.TS.Add(time.Second)
	hb.Ports = []agentproto.PortCounter{identified, legacy} // Order is irrelevant.
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	requireAccurateTotals(t, st, store.SubjectShare, sh.ID, *now, 50, 50)
	hb.TS = hb.TS.Add(time.Second)
	identified.Rx, identified.Tx, identified.FromZero = 175, 280, true
	hb.Ports = []agentproto.PortCounter{identified}
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	duplicate := identified
	duplicate.Source, duplicate.Epoch = "nft-node-v1", "new-nft"
	hb.Ports = []agentproto.PortCounter{identified, duplicate}
	if _, err := ing.Ingest(ctx, server, hb); err == nil {
		t.Fatal("same node's systemd and nft counters were both charged")
	}
	requireAccurateTotals(t, st, store.SubjectNode, nodes[0].ID, *now, 75, 80)
	requireAccurateTotals(t, st, store.SubjectShare, sh.ID, *now, 75, 80)
}

func TestManualShareResetKeepsDelayedHistoryOutOfCurrentQuota(t *testing.T) {
	for _, resetDay := range []int{0, 1} {
		t.Run(fmt.Sprint(resetDay), func(t *testing.T) {
			st, ing, server, sh, nodes, now := accurateLedgerFixture(t, 1, resetDay)
			ctx := context.Background()
			if err := st.ResetShareUsage(ctx, sh.ID, *now); err != nil {
				t.Fatal(err)
			}
			batch := agentproto.MeterSettlement{TS: now.Add(-time.Hour), Counters: []agentproto.PortCounter{{NodeID: nodes[0].ID, Source: "nft-node-v1", Epoch: "fixture:1", FromZero: true, Rx: 100, Tx: 200}}}
			batch.ID = batch.Digest()
			for repeat := 0; repeat < 2; repeat++ {
				result, err := ing.Ingest(ctx, server, agentproto.Heartbeat{FinalMeters: &batch})
				if err != nil || result.FinalMeterAck != batch.ID {
					t.Fatal("delayed final snapshot lost its transactional acknowledgment", err)
				}
			}
			live := batch.Counters[0]
			live.Rx, live.Tx = 150, 270
			if _, err := ing.Ingest(ctx, server, agentproto.Heartbeat{TS: now.Add(time.Second), Ports: []agentproto.PortCounter{live}}); err != nil {
				t.Fatal(err)
			}
			got, err := st.GetShare(ctx, sh.ID)
			if err != nil || got.UsedUpload != 50 || got.UsedDownload != 70 || !got.PeriodStart.Equal(*now) {
				t.Fatal("delayed previous interval consumed the manually reset quota", got.UsedUpload, got.UsedDownload, err)
			}
			requireAccurateTotals(t, st, store.SubjectShare, sh.ID, *now, 150, 270)
		})
	}
}

func TestSharedLedgerAggregatesRawDirectionsAcrossServersBeforeBilling(t *testing.T) {
	st, ing, server, sh, nodes, now := accurateLedgerFixture(t, 1, 0)
	ctx := context.Background()
	second := domain.Server{Name: "second fixture", Enabled: true}
	if err := st.CreateServer(ctx, &second); err != nil {
		t.Fatal(err)
	}
	node := nodes[0]
	node.ID, node.ServerID = 0, &second.ID
	if err := st.CreateNode(ctx, &node); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		server domain.Server
		node   domain.Node
		rx, tx int64
	}{{server, nodes[0], 100, 900}, {second, node, 800, 200}} {
		hb := agentproto.Heartbeat{TS: *now, Ports: []agentproto.PortCounter{{NodeID: sample.node.ID, Source: "nft-node-v1", Epoch: "fixture:same-generation", FromZero: true, Rx: sample.rx, Tx: sample.tx}}}
		for repeat := 0; repeat < 2; repeat++ {
			if _, err := ing.Ingest(ctx, sample.server, hb); err != nil {
				t.Fatal(err)
			}
		}
		requireAccurateTotals(t, st, store.SubjectNode, sample.node.ID, *now, sample.rx, sample.tx)
		requireAccurateTotals(t, st, store.SubjectServer, sample.server.ID, *now, 0, 0)
	}
	requireAccurateTotals(t, st, store.SubjectShare, sh.ID, *now, 900, 1100)
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.UsedUpload != 900 || got.UsedDownload != 1100 || domain.QuotaBilled(domain.BillingMax, got.UsedUpload, got.UsedDownload) != 1100 {
		t.Fatal("cross-server share was scaled or billed per source", err)
	}
	series, err := ing.Daily(ctx, store.SubjectShare, sh.ID, 1)
	if err != nil || series.TotalUp != 900 || series.TotalDown != 1100 || series.Total != 2000 {
		t.Fatal("share chart disagrees with its raw ledger", err)
	}
}

func TestTrafficAggregateOverflowRollsBackEveryBaselineAndLedger(t *testing.T) {
	st, ing, server, sh, nodes, now := accurateLedgerFixture(t, 3, 0)
	ctx := context.Background()
	hb := agentproto.Heartbeat{TS: *now}
	for _, node := range nodes {
		hb.Ports = append(hb.Ports, agentproto.PortCounter{NodeID: node.ID, Source: "nft-node-v1", Epoch: "fixture:1", FromZero: true, Rx: math.MaxInt64 / 4, Tx: math.MaxInt64 / 4})
	}
	result, err := ing.Ingest(ctx, server, hb)
	if err == nil || len(result.NodeDeltas) != 0 || len(result.Shares) != 0 {
		t.Fatal("overflow escaped as a committed result", err)
	}
	for _, table := range []string{"counter_state", "traffic_samples", "traffic_hourly", "traffic_daily"} {
		var count int
		if err := st.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("overflow transaction left partial data", table, count, err)
		}
	}
	hb.Ports[2].Rx, hb.Ports[2].Tx = 0, 0
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal("valid retry could not recover rolled-back baselines", err)
	}
	want := int64(2 * (math.MaxInt64 / 4))
	requireAccurateTotals(t, st, store.SubjectShare, sh.ID, *now, want, want)
	hb.TS = hb.TS.Add(time.Second)
	hb.Ports[2].Rx, hb.Ports[2].Tx = math.MaxInt64/4, math.MaxInt64/4
	if _, err := ing.Ingest(ctx, server, hb); err == nil {
		t.Fatal("existing share/bucket totals overflowed on the next report")
	}
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.UsedUpload != want || got.UsedDownload != want {
		t.Fatal("overflow changed exact quota usage", err)
	}
	requireAccurateTotals(t, st, store.SubjectShare, sh.ID, *now, want, want)
}

func TestTrafficSeriesRejectsUnrepresentableWindowTotal(t *testing.T) {
	st, ing, server, _, _, now := accurateLedgerFixture(t, 0, 0)
	ctx := context.Background()
	half := int64(math.MaxInt64 / 2)
	if err := st.AddTraffic(ctx, store.SubjectServer, server.ID, now.Add(-24*time.Hour), half, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTraffic(ctx, store.SubjectServer, server.ID, *now, 0, half+2); err != nil {
		t.Fatal(err)
	}
	if _, err := ing.Daily(ctx, store.SubjectServer, server.ID, 2); err == nil {
		t.Fatal("daily series published an overflowing total")
	}
}
