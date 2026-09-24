package traffic_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

func TestForwardAccountingIsSeparateReplaySafeAndAtomic(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "forward.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	server := domain.Server{Name: "fixture", Enabled: true, PublicHost: "192.0.2.1", CoreMode: domain.CoreModeStable}
	if err := st.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	other := domain.Server{Name: "other", Enabled: true, PublicHost: "192.0.2.2", CoreMode: domain.CoreModeStable}
	if err := st.CreateServer(ctx, &other); err != nil {
		t.Fatal(err)
	}
	config := networkconfig.Forward{ListenMode: "all", ListenPort: 25001, Network: "tcp", TargetHost: "203.0.113.10", TargetPort: 443, SourceMode: "cidr", MaxTCPConnections: 2}
	create := func(sid int64, opID string) int64 {
		t.Helper()
		op, err := st.RequestPortForward(ctx, store.PortForwardRequest{ID: strings.Repeat(opID, 32), Action: "create", ServerID: sid, Name: "fixture", Enabled: true, Config: &config}, domain.AuditEvent{})
		if err != nil {
			t.Fatal(err)
		}
		return op.ResourceID
	}
	id, foreign := create(server.ID, "a"), create(other.ID, "b")
	ing := traffic.New(st)
	ing.Now = st.Now
	hb := agentproto.Heartbeat{TS: now, ForwardCounters: []agentproto.ForwardCounter{{ForwardID: id, Epoch: "boot:forward-1", Rx: 100, Tx: 200}}}
	apply := func() {
		t.Helper()
		result, err := ing.Ingest(ctx, server, hb)
		if err != nil || len(result.Shares) != 0 || len(result.NodeDeltas) != 0 || result.ServerUp != 0 || result.ServerDown != 0 {
			t.Fatal("forward entered another ledger", result, err)
		}
	}
	assertUsage := func(rx, tx int64) {
		t.Helper()
		for _, subject := range []string{store.SubjectForward, store.SubjectNode, store.SubjectShare, store.SubjectServer} {
			wantRx, wantTx := int64(0), int64(0)
			if subject == store.SubjectForward {
				wantRx, wantTx = rx, tx
			}
			gotRx, gotTx, err := st.SumTraffic(ctx, subject, id, now.Add(-time.Hour), now.Add(time.Hour))
			if err != nil || gotRx != wantRx || gotTx != wantTx {
				t.Fatalf("%s: %d/%d != %d/%d: %v", subject, gotRx, gotTx, wantRx, wantTx, err)
			}
		}
	}
	apply()
	apply()
	assertUsage(100, 200)
	hb.TS = now.Add(time.Second)
	hb.ForwardCounters[0].Rx = 150
	hb.ForwardCounters = append(hb.ForwardCounters, agentproto.ForwardCounter{ForwardID: foreign, Epoch: "boot:foreign", Rx: 5})
	if _, err := ing.Ingest(ctx, server, hb); err == nil {
		t.Fatal("foreign forward accepted")
	}
	assertUsage(100, 200)
	hb.ForwardCounters = hb.ForwardCounters[:1]
	apply()
	assertUsage(150, 200)
	old := hb.ForwardCounters[0]
	hb.ForwardCounters[0] = agentproto.ForwardCounter{ForwardID: id, Epoch: "boot:forward-2", Rx: 3, Tx: 4}
	apply()
	apply()
	assertUsage(153, 204)
	hb.ForwardCounters[0] = old
	apply()
	assertUsage(153, 204)
	hb.ForwardCounters[0].Rx--
	if _, err := ing.Ingest(ctx, server, hb); err == nil {
		t.Fatal("counter reset reused its epoch")
	}
	assertUsage(153, 204)
}
