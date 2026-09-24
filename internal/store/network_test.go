package store

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"strings"
	"testing"
	"time"
)

func networkFixture() *agentproto.NetworkSnapshot {
	return &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "boot-a", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok", Interfaces: []agentproto.NetworkInterface{
		{ID: strings.Repeat("b", 32), Generation: strings.Repeat("c", 32), Name: "eth0", Index: 2, Kind: "physical", MTU: 1500, CountersValid: true, Rx: 100, Tx: 200},
		{ID: strings.Repeat("d", 32), Generation: strings.Repeat("e", 32), Name: "wg0", Index: 3, Kind: "wireguard", MTU: 1420, CountersValid: true, Rx: 300, Tx: 400},
	}}
}

func TestNetworkAccountingIsIsolatedAndReplaySafe(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	srv := &domain.Server{Name: "network-test", Enabled: true}
	if err := s.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	n := networkFixture()
	ingest := func() {
		t.Helper()
		if err := s.IngestNetwork(ctx, srv.ID, n); err != nil {
			t.Fatal(err)
		}
	}
	check := func(wantRx, wantTx int64) {
		t.Helper()
		v, err := s.Network(ctx, srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		var rx, tx int64
		for _, l := range v.Interfaces {
			a, b, err := s.SumTraffic(ctx, SubjectInterface, l.ID, s.Now().Add(-time.Hour), s.Now())
			if err != nil {
				t.Fatal(err)
			}
			rx += a
			tx += b
		}
		if rx != wantRx || tx != wantTx {
			t.Fatalf("inventory usage %d/%d want %d/%d", rx, tx, wantRx, wantTx)
		}
		a, b, err := s.SumTraffic(ctx, SubjectServer, srv.ID, s.Now().Add(-time.Hour), s.Now())
		if err != nil || a != 0 || b != 0 {
			t.Fatalf("observation affected quota %d/%d %v", a, b, err)
		}
	}
	ingest()
	check(0, 0)
	n.Sequence++
	n.Interfaces[0].Rx += 10
	n.Interfaces[0].Tx += 20
	n.Interfaces[1].Rx += 30
	n.Interfaces[1].Tx += 40
	ingest()
	check(40, 60)
	ingest()
	check(40, 60) // retry
	n.Sequence = 1
	n.Interfaces[0].Rx += 1000
	ingest()
	check(40, 60) // stale data must not move the baseline
	n.Sequence = 3
	n.BootID = "boot-b"
	ingest()
	check(40, 60)
	n.BootID = "boot-a"
	n.Sequence = 999
	ingest()
	check(40, 60) // retired boot never becomes current again
	n.BootID = "unseen-old-boot"
	n.Sequence = 2
	ingest()
	v, err := s.Network(ctx, srv.ID)
	if err != nil || v.Snapshot.BootID != "boot-b" {
		t.Fatal("unseen stale boot replaced current observation", err)
	}
	n.BootID = "boot-b"
	n.Sequence = 4
	n.Interfaces[0].Rx = 1
	n.Interfaces[0].Tx = 1
	ingest()
	check(40, 60) // backwards counters establish a new baseline
}

func TestNetworkTransactionAndMissingInterfaces(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	srv := &domain.Server{Name: "network-test"}
	if err := s.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	n := networkFixture()
	if err := s.IngestNetwork(ctx, srv.ID, n); err != nil {
		t.Fatal(err)
	}
	n.Sequence++
	n.Interfaces[0].Rx += 50
	if _, err := s.db.Exec(`CREATE TRIGGER fail_network BEFORE INSERT ON traffic_daily WHEN NEW.subject='interface' BEGIN SELECT RAISE(ABORT,'test'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.IngestNetwork(ctx, srv.ID, n); err == nil {
		t.Fatal("expected write failure")
	}
	v, err := s.Network(ctx, srv.ID)
	if err != nil || v.Snapshot.Sequence != 1 {
		t.Fatal("snapshot advanced on rollback", err)
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_network"); err != nil {
		t.Fatal(err)
	}
	if err = s.IngestNetwork(ctx, srv.ID, n); err != nil {
		t.Fatal(err)
	}
	n.Sequence++
	n.Interfaces = nil
	n.Status = "incomplete"
	if err = s.IngestNetwork(ctx, srv.ID, n); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Network(ctx, srv.ID)
	for _, l := range v.Interfaces {
		if !l.Present {
			t.Fatal("incomplete dump retired interface")
		}
	}
	n.Sequence++
	n.Status = "ok"
	if err = s.IngestNetwork(ctx, srv.ID, n); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Network(ctx, srv.ID)
	for _, l := range v.Interfaces {
		if l.Present {
			t.Fatal("missing interface still present")
		}
	}
	if _, err = s.db.Exec("DELETE FROM servers WHERE id=?", srv.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM traffic_daily WHERE subject='interface'").Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan observation history", count, err)
	}
}
