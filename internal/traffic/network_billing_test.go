package traffic_test

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

func TestInterfaceCumulativeOverflowAcrossPeriodsRollsBackCutover(t *testing.T) {
	ctx := context.Background()
	st, ing, server, _, _, now := accurateLedgerFixture(t, 0, 0)
	q := int64(math.MaxInt64 / 4)
	hb := agentproto.Heartbeat{TS: *now, Epoch: "boot", Metrics: agentproto.Metrics{NetRx: q, NetTx: q}}
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	ids := []string{strings.Repeat("a", 32), strings.Repeat("e", 32)}
	policy, err := st.RequestNetworkBilling(ctx, server.ID, 0, agentproto.NetworkBillingPolicy{Mode: "interfaces", InterfaceIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("b", 32), BootID: "boot", Sequence: 1, SampledAt: *now, Status: "ok", Interfaces: []agentproto.NetworkInterface{
		{ID: ids[0], Generation: strings.Repeat("c", 32), Name: "eth0", Index: 2, Kind: "physical", CountersValid: true},
		{ID: ids[1], Generation: strings.Repeat("c", 32), Name: "eth1", Index: 3, Kind: "physical", CountersValid: true},
	}}
	sw := &agentproto.NetworkBillingSwitch{ID: strings.Repeat("d", 32), Next: policy, TS: *now, Legacy: agentproto.LegacyNetworkCounters{Epoch: "boot", Rx: q, Tx: q}, Snapshot: snapshot}
	if result, err := ing.Ingest(ctx, server, agentproto.Heartbeat{NetworkBillingSwitch: sw}); err != nil || result.NetworkBillingAck != sw.ID {
		t.Fatal(result, err)
	}
	next := *snapshot
	next.Interfaces = append([]agentproto.NetworkInterface(nil), snapshot.Interfaces...)
	next.Sequence++
	next.Interfaces[0].Rx, next.Interfaces[0].Tx = q, q
	hb.TS = now.Add(time.Second)
	hb.NetworkBillingRevision, hb.Metrics.Network = policy.Revision, &next
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	// Each period's ledger and each direction remain representable. Only the
	// chart's continuous cumulative inbound+outbound now exceeds MaxInt64.
	*now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	next.Sequence++
	next.SampledAt = *now
	next.Interfaces[1].Rx, next.Interfaces[1].Tx = 1, 3
	legacy, err := st.RequestNetworkBilling(ctx, server.ID, policy.Revision, agentproto.LegacyNetworkBilling())
	if err != nil {
		t.Fatal(err)
	}
	boundary := &agentproto.NetworkBillingSwitch{ID: strings.Repeat("f", 32), PreviousRevision: policy.Revision, Next: legacy, TS: *now, Legacy: sw.Legacy, Snapshot: &next}
	result, err := ing.Ingest(ctx, server, agentproto.Heartbeat{NetworkBillingSwitch: boundary})
	if err == nil || result.NetworkBillingAck != "" || result.ServerUp != 0 || result.ServerDown != 0 {
		t.Fatal("overflow committed or exposed a settlement", result, err)
	}
	view, err := st.NetworkBilling(ctx, server.ID)
	if err != nil || view.Current.Revision != policy.Revision {
		t.Fatal("failed cutover changed active policy", view, err)
	}
	var rx, tx, sequence int64
	if err := st.DB().QueryRow("SELECT rx,tx,sequence FROM network_billing_baselines WHERE server_id=? AND interface_id=?", server.ID, ids[1]).Scan(&rx, &tx, &sequence); err != nil || rx != 0 || tx != 0 || sequence != 2 {
		t.Fatal("failed sample advanced the interface baseline", rx, tx, sequence, err)
	}
	if rx, tx, err := st.SumTraffic(ctx, store.SubjectServer, server.ID, *now, *now); err != nil || rx != 0 || tx != 0 {
		t.Fatal("failed sample consumed the new period", rx, tx, err)
	}
	samples, err := st.ListSamples(ctx, server.ID, nil, now.AddDate(0, -1, 0))
	if err != nil || len(samples) != 2 {
		t.Fatal("invalid cumulative sample persisted", len(samples), err)
	}
}

func TestLegacyReplaySeedsRequestedBillingWithoutDuplicatingSamples(t *testing.T) {
	ctx := context.Background()
	st, ing, server, _, _, now := accurateLedgerFixture(t, 0, 0)
	id := strings.Repeat("a", 32)
	snapshot := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("b", 32), BootID: "boot", Sequence: 1, SampledAt: *now, Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: strings.Repeat("c", 32), Name: "eth0", Index: 2, Kind: "physical", CountersValid: true, Rx: 500, Tx: 600}}}
	hb := agentproto.Heartbeat{TS: *now, Epoch: "boot", Metrics: agentproto.Metrics{NetRx: 100, NetTx: 200, Network: snapshot}}
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	policy, err := st.RequestNetworkBilling(ctx, server.ID, 0, agentproto.NetworkBillingPolicy{Mode: "interfaces", InterfaceIDs: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	sw := &agentproto.NetworkBillingSwitch{ID: strings.Repeat("d", 32), Next: policy, TS: *now, Legacy: agentproto.LegacyNetworkCounters{Epoch: "boot", Rx: 100, Tx: 200}, Snapshot: snapshot}
	for repeat := 0; repeat < 2; repeat++ {
		result, err := ing.Ingest(ctx, server, agentproto.Heartbeat{NetworkBillingSwitch: sw})
		if err != nil || result.NetworkBillingAck != sw.ID {
			t.Fatal("unchanged boundary rejected", result, err)
		}
	}
	next := *snapshot
	next.Interfaces = append([]agentproto.NetworkInterface(nil), snapshot.Interfaces...)
	next.Sequence++
	next.Interfaces[0].Rx, next.Interfaces[0].Tx = 510, 620
	hb.TS = now.Add(time.Second)
	hb.NetworkBillingRevision = policy.Revision
	hb.Metrics.Network = &next
	if _, err = ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	requireAccurateTotals(t, st, store.SubjectServer, server.ID, *now, 10, 20)
	samples, err := st.ListSamples(ctx, server.ID, nil, now.Add(-time.Hour))
	if err != nil || len(samples) != 2 || samples[len(samples)-1].RxBytes != 110 || samples[len(samples)-1].TxBytes != 220 {
		t.Fatal("replay changed chart baseline or duplicated samples", samples, err)
	}
}

func TestNetworkBillingCutoverReplayMissingAndReturnToLegacy(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "billing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 20, 23, 59, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	srv := domain.Server{Name: "billing"}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	i := traffic.New(st)
	i.Now = st.Now
	ingest := func(h agentproto.Heartbeat) traffic.Result {
		t.Helper()
		r, e := i.Ingest(ctx, srv, h)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	assertUsage := func(rx, tx int64) {
		t.Helper()
		a, b, e := st.SumTraffic(ctx, store.SubjectServer, srv.ID, now.Add(-24*time.Hour), now.Add(24*time.Hour))
		if e != nil || a != rx || b != tx {
			t.Fatalf("got %d/%d want %d/%d: %v", a, b, rx, tx, e)
		}
	}
	legacy := agentproto.Heartbeat{Epoch: "boot:nonce", TS: now, Metrics: agentproto.Metrics{NetRx: 100, NetTx: 200}}
	ingest(legacy)
	assertUsage(0, 0)
	ids := []string{strings.Repeat("b", 32), strings.Repeat("d", 32)}
	p, err := st.RequestNetworkBilling(ctx, srv.ID, 0, agentproto.NetworkBillingPolicy{Mode: "interfaces", InterfaceIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "boot", Sequence: 1, SampledAt: now, Status: "ok", Interfaces: []agentproto.NetworkInterface{
		{ID: ids[0], Generation: strings.Repeat("c", 32), Name: "eth0", Index: 2, Kind: "physical", CountersValid: true, Rx: 10000, Tx: 20000},
		{ID: ids[1], Generation: strings.Repeat("e", 32), Name: "eth1", Index: 3, Kind: "physical", CountersValid: true, Rx: 30000, Tx: 40000},
	}}
	now = now.Add(time.Second)
	sw := &agentproto.NetworkBillingSwitch{ID: strings.Repeat("1", 32), PreviousRevision: 0, Next: p, TS: now, Legacy: agentproto.LegacyNetworkCounters{Epoch: legacy.Epoch, Rx: 150, Tx: 270}, Snapshot: snapshot}
	packet := agentproto.Heartbeat{NetworkBillingSwitch: sw}
	if _, err = st.DB().Exec(`CREATE TRIGGER fail_billing BEFORE INSERT ON network_billing_settlements BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = i.Ingest(ctx, srv, packet); err == nil {
		t.Fatal("expected failed settlement")
	}
	assertUsage(0, 0)
	view, _ := st.NetworkBilling(ctx, srv.ID)
	if view.Current.Revision != 0 {
		t.Fatal("cutover escaped failed transaction")
	}
	// Live heartbeats may still carry node counters while the frozen server
	// boundary awaits acknowledgment. They must not advance either server set.
	ingest(agentproto.Heartbeat{TS: now, Epoch: legacy.Epoch, NetworkBillingHeld: true, Metrics: agentproto.Metrics{NetRx: 900000, NetTx: 900000}})
	assertUsage(0, 0)
	if _, err = st.DB().Exec("DROP TRIGGER fail_billing"); err != nil {
		t.Fatal(err)
	}
	if r := ingest(packet); r.NetworkBillingAck != sw.ID {
		t.Fatal("missing committed ACK")
	}
	assertUsage(50, 70)
	ingest(packet)
	assertUsage(50, 70)
	ingest(agentproto.Heartbeat{TS: now, NetworkBillingHeld: true, Metrics: agentproto.Metrics{NetRx: 950000, NetTx: 950000}})
	assertUsage(50, 70)
	// Late delivery keeps the frozen old-period sample in its original date.
	now = now.Add(2 * time.Minute)
	ingest(packet)
	assertUsage(50, 70)
	buckets, err := st.DailyTraffic(ctx, store.SubjectServer, srv.ID, now.Add(-24*time.Hour), now)
	if err != nil || len(buckets) != 1 || buckets[0].Bucket.Day() != 20 {
		t.Fatal("late settlement moved period", buckets, err)
	}
	// Never modify the frozen packet while retrying it.
	copySnapshot := *snapshot
	copySnapshot.Interfaces = append([]agentproto.NetworkInterface(nil), snapshot.Interfaces...)
	snapshot = &copySnapshot
	snapshot.Sequence++
	snapshot.Interfaces[0].Rx += 10
	snapshot.Interfaces[0].Tx += 20
	snapshot.Interfaces[1].Rx += 30
	snapshot.Interfaces[1].Tx += 40
	hb := agentproto.Heartbeat{TS: now, NetworkBillingRevision: p.Revision, Epoch: legacy.Epoch, Metrics: agentproto.Metrics{NetRx: 999999, NetTx: 999999, Network: snapshot}}
	ingest(hb)
	assertUsage(90, 130)
	ingest(hb)
	assertUsage(90, 130)
	// A downgrade cannot silently reactivate the legacy source.
	oldAgent := legacy
	oldAgent.TS = now
	oldAgent.Metrics.NetRx = 999999
	ingest(oldAgent)
	assertUsage(90, 130)
	snapshot.Sequence++
	snapshot.Interfaces = snapshot.Interfaces[:1]
	snapshot.Interfaces[0].Rx += 5
	snapshot.Interfaces[0].Tx += 6
	ingest(hb)
	assertUsage(95, 136)
	view, _ = st.NetworkBilling(ctx, srv.ID)
	if view.Status != "incomplete" {
		t.Fatal("missing interface not reported")
	}
	snapshot.Sequence++
	snapshot.Interfaces[0].Generation = strings.Repeat("f", 32)
	snapshot.Interfaces[0].Rx = 1
	snapshot.Interfaces[0].Tx = 2
	ingest(hb)
	assertUsage(95, 136)
	p2, err := st.RequestNetworkBilling(ctx, srv.ID, 1, agentproto.LegacyNetworkBilling())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence++
	snapshot.Interfaces[0].Rx = 4
	snapshot.Interfaces[0].Tx = 6
	sw2 := &agentproto.NetworkBillingSwitch{ID: strings.Repeat("2", 32), PreviousRevision: 1, Next: p2, TS: now, Legacy: agentproto.LegacyNetworkCounters{Epoch: legacy.Epoch, Rx: 50000, Tx: 60000}, Snapshot: snapshot}
	ingest(agentproto.Heartbeat{NetworkBillingSwitch: sw2})
	assertUsage(98, 140)
	now = now.Add(time.Second)
	ingest(agentproto.Heartbeat{TS: now, NetworkBillingRevision: 2, NetworkBillingLegacy: &agentproto.LegacyNetworkCounters{Epoch: legacy.Epoch, Rx: 50007, Tx: 60008}})
	assertUsage(105, 148)
	// Receipt identity is content-bound.
	altered := *sw
	altered.Legacy.Rx++
	if _, err = i.Ingest(ctx, srv, agentproto.Heartbeat{NetworkBillingSwitch: &altered}); err == nil {
		t.Fatal("changed replay accepted")
	}
}

func TestNetworkBillingRuntimeTopologyAndRetiredCollector(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "topology.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := domain.Server{Name: "fixture"}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	ing := traffic.New(st)
	id1, id2 := strings.Repeat("a", 32), strings.Repeat("b", 32)
	policy, err := st.RequestNetworkBilling(ctx, srv.ID, 0, agentproto.NetworkBillingPolicy{Mode: "interfaces", InterfaceIDs: []string{id1, id2}})
	if err != nil {
		t.Fatal(err)
	}
	snap := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("c", 32), BootID: "boot", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok", Interfaces: []agentproto.NetworkInterface{
		{ID: id1, Generation: strings.Repeat("d", 32), Index: 2, Name: "wan0", Kind: "physical", CountersValid: true, Rx: 100, Tx: 200},
		{ID: id2, Generation: strings.Repeat("e", 32), Index: 3, Name: "wan1", Kind: "physical", CountersValid: true, Rx: 300, Tx: 400},
	}}
	sw := &agentproto.NetworkBillingSwitch{ID: strings.Repeat("f", 32), Next: policy, TS: snap.SampledAt, Legacy: agentproto.LegacyNetworkCounters{Epoch: "boot"}, Snapshot: snap}
	if _, err = ing.Ingest(ctx, srv, agentproto.Heartbeat{NetworkBillingSwitch: sw}); err != nil {
		t.Fatal(err)
	}
	ingest := func(want int64) {
		t.Helper()
		snap.Sequence++
		r, e := ing.Ingest(ctx, srv, agentproto.Heartbeat{TS: time.Now().UTC(), NetworkBillingRevision: policy.Revision, Metrics: agentproto.Metrics{Network: snap}})
		if e != nil || r.ServerUp != want {
			t.Fatalf("delta %d want %d: %v", r.ServerUp, want, e)
		}
	}
	snap.Interfaces[0].Rx += 10
	ingest(10)
	// Live topology change would count the same packets at two layers.
	snap.Interfaces[1].MasterIndex = 2
	snap.Interfaces[0].Rx += 20
	ingest(0)
	v, _ := st.NetworkBilling(ctx, srv.ID)
	if v.Status != "incomplete" || !strings.Contains(v.Error, "上下级") {
		t.Fatal(v)
	}
	snap.Interfaces[1].MasterIndex = 0
	snap.Interfaces[0].Rx += 30
	ingest(0) // fresh baseline; do not charge invalid interval
	snap.Interfaces[0].Rx += 5
	ingest(5)
	// A new registry retires the old registry. Its delayed sample cannot
	// reactivate selected identities that no longer exist in the new registry.
	old := *snap
	old.Interfaces = append([]agentproto.NetworkInterface(nil), snap.Interfaces...)
	snap.CollectorID = strings.Repeat("1", 32)
	snap.Sequence = 1
	snap.Interfaces = nil
	ingest(0)
	snap = &old
	snap.Interfaces[0].Rx += 1000
	ingest(0)
	v, _ = st.NetworkBilling(ctx, srv.ID)
	if v.Status != "incomplete" {
		t.Fatal("retired source overwrote health")
	}
}

func TestHeldServerBillingDoesNotPauseShareMeters(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "shares.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := domain.Server{Name: "fixture", PublicHost: "192.0.2.1", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	manager := share.New(st, desired.New(st))
	sh := domain.Share{Name: "fixture", ResetDay: 1, Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}}
	if _, err = manager.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil {
		t.Fatal(err)
	}
	ing := traffic.New(st)
	if _, err = st.RequestNetworkBilling(ctx, srv.ID, 0, agentproto.LegacyNetworkBilling()); err != nil {
		t.Fatal(err)
	}
	hb := agentproto.Heartbeat{TS: time.Now().UTC(), NetworkBillingHeld: true, Metrics: agentproto.Metrics{NetRx: 9000, NetTx: 9000}, Ports: []agentproto.PortCounter{{NodeID: nodes[0].ID, Source: "nft-node-v1", Epoch: "fixture", FromZero: true, Rx: 100, Tx: 200}}}
	for j := 0; j < 2; j++ {
		r, e := ing.Ingest(ctx, srv, hb)
		if e != nil || r.ServerUp != 0 || r.ServerDown != 0 {
			t.Fatal(r, e)
		}
	}
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.UsedUpload != 100 || got.UsedDownload != 200 {
		t.Fatal(got.UsedUpload, got.UsedDownload, err)
	}
	v, err := st.NetworkBilling(ctx, srv.ID)
	if err != nil || v.Status != "switching" {
		t.Fatal(v, err)
	}
	events, err := st.ListAudit(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Action == "server.network_billing.health" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("unchanged gap generated repeated audit events", count)
	}
}
