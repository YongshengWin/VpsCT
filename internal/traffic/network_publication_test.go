package traffic_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

func TestConcurrentNetworkWriteDoesNotAdvanceFailedAccountingBaseline(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "contention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	server := domain.Server{Name: "fixture", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	manager := share.New(s, desired.New(s))
	manager.Now = s.Now
	sh := domain.Share{Name: "fixture", ResetDay: 1, Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	if _, err = manager.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil || len(nodes) != 1 {
		t.Fatal(err)
	}
	n := nodes[0]
	if _, err = s.SetNodeNetwork(ctx, n.ID, 0, &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}, nil); err != nil {
		t.Fatal(err)
	}
	ing := traffic.New(s)
	ing.Now = s.Now
	hb := agentproto.Heartbeat{TS: now, Epoch: "boot", Metrics: agentproto.Metrics{NetRx: 1000, NetTx: 2000}, Ports: []agentproto.PortCounter{{NodeID: n.ID, Port: n.ListenPort, Source: "nft-node-v1", Epoch: "boot:1", FromZero: true, Rx: 100, Tx: 200}}}
	if _, err = ing.Ingest(ctx, server, hb); err != nil {
		t.Fatal(err)
	}
	// Hold an actual SQLite writer, as an overlapping network edit/publication
	// can do. The accounting transaction must fail without advancing baselines.
	writer, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.ExecContext(ctx, `UPDATE server_network_generations SET generation=generation+1 WHERE server_id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	hb.TS = now.Add(time.Second)
	hb.Ports[0].Rx, hb.Ports[0].Tx = 150, 270
	hb.Metrics.NetRx, hb.Metrics.NetTx = 1500, 2500
	blocked, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	_, err = ing.Ingest(blocked, server, hb)
	cancel()
	if err == nil {
		t.Fatal("accounting unexpectedly committed through competing writer")
	}
	got, err := s.GetShare(ctx, sh.ID)
	if err != nil || got.UsedUpload != 100 || got.UsedDownload != 200 {
		t.Fatal("failed transaction partly changed share usage", err)
	}
	if err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
	// The next larger cumulative report includes the skipped increment. A
	// duplicate report must not charge it twice; this covers a stable epoch.
	hb.TS = now.Add(2 * time.Second)
	hb.Ports[0].Rx, hb.Ports[0].Tx = 210, 350
	hb.Metrics.NetRx, hb.Metrics.NetTx = 1700, 2900
	for i := 0; i < 2; i++ {
		if _, err = ing.Ingest(ctx, server, hb); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.GetShare(ctx, sh.ID)
	if err != nil || got.UsedUpload != 210 || got.UsedDownload != 350 {
		t.Fatal("next cumulative report lost or duplicated share usage", err)
	}
	for _, want := range []struct {
		subject    string
		id, rx, tx int64
	}{{store.SubjectNode, n.ID, 210, 350}, {store.SubjectServer, server.ID, 700, 900}} {
		rx, tx, err := s.SumTraffic(ctx, want.subject, want.id, now.Add(-time.Hour), now.Add(time.Hour))
		if err != nil || rx != want.rx || tx != want.tx {
			t.Fatalf("%s ledger lost or duplicated the skipped sample: %d/%d, %v", want.subject, rx, tx, err)
		}
	}
}
