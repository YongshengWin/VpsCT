package share

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"ctlvps/internal/traffic"
)

func setup(t *testing.T) (*Manager, *store.Store, domain.Server, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	st.Now = clock
	d := desired.New(st)
	d.Now = clock
	m := New(st, d)
	m.Now = clock
	srv := domain.Server{Name: "hk", PublicHost: "1.2.3.4", Enabled: true, CoreMode: domain.CoreModeStable}
	if err := st.CreateServer(context.Background(), &srv); err != nil {
		t.Fatal(err)
	}
	return m, st, srv, &now
}

func TestShareLifecycle(t *testing.T) {
	m, st, srv, now := setup(t)
	ctx := context.Background()
	sh := &domain.Share{Name: "alice", QuotaBytes: 1000, BillingMode: "sum", ResetDay: 1,
		Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless", "snell", "hysteria2"}}}}
	token, err := m.Create(ctx, sh)
	if err != nil || token == "" {
		t.Fatalf("create: %v", err)
	}
	nodes, _ := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if len(nodes) != 3 {
		t.Fatalf("expected 3 dedicated nodes, got %d", len(nodes))
	}
	cores := map[domain.Core]int{}
	for _, n := range nodes {
		cores[n.Core]++
		if n.ListenPort == 0 || n.Source != domain.NodeDeployed {
			t.Fatalf("bad node %+v", n)
		}
	}
	if cores[domain.CoreSnell] != 1 || cores[domain.CoreSingBox] != 2 {
		t.Fatalf("core split wrong: %v", cores)
	}
	rec, err := st.LatestDesiredState(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	ds, _ := desired.Load(rec)
	if len(ds.Nodes) != 3 || ds.Revision != 1 {
		t.Fatalf("desired: %+v", ds)
	}
	if ds.Nodes[0].Blocked {
		t.Fatal("active share must not be blocked")
	}
	sub, err := st.GetSubscriptionByShare(ctx, sh.ID)
	if err != nil || sub.Kind != domain.SubShare {
		t.Fatalf("subscription: %v", err)
	}
	if len(sub.ProxyGroups) != 0 || len(sub.Rules) != 0 {
		t.Fatalf("share must follow the template, not stock 节点选择: %+v %v", sub.ProxyGroups, sub.Rules)
	}
	// old rows that still have the stock groups must not hide ⚡️ smart
	sub.ProxyGroups = []domain.ProxyGroup{{Name: "节点选择", Type: "select", IncludeAll: true}}
	sub.Rules = []string{"MATCH,节点选择"}
	rendered, _, err := subscription.NewService(st).Render(ctx, sub, subscription.FormatMihomo)
	if err != nil {
		t.Fatal(err)
	}
	body := string(rendered.Body)
	if !strings.Contains(body, "⚡️ smart") || !strings.Contains(body, "rule-providers:") {
		t.Fatalf("share profile missing template groups:\n%s", body[:min(len(body), 800)])
	}
	if strings.Contains(body, "name: 节点选择") {
		t.Fatal("stock 节点选择 leaked over the template")
	}

	// consume below quota: stays active
	if err := m.ApplyDeltas(ctx, []traffic.ShareDelta{{ShareID: sh.ID, Up: 300, Down: 300}}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetShare(ctx, sh.ID)
	if got.Status != domain.ShareActive || got.UsedUpload != 300 {
		t.Fatalf("%+v", got)
	}
	// exceed quota: exhausted + blocked in desired state (sum bills outbound only)
	if err := m.ApplyDeltas(ctx, []traffic.ShareDelta{{ShareID: sh.ID, Up: 300, Down: 800}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetShare(ctx, sh.ID)
	if got.Status != domain.ShareExhausted {
		t.Fatalf("expected exhausted, got %s", got.Status)
	}
	rec, _ = st.LatestDesiredState(ctx, srv.ID)
	ds, _ = desired.Load(rec)
	if rec.Revision != 2 || !ds.Nodes[0].Blocked {
		t.Fatalf("exhausted share must block nodes: rev=%d blocked=%v", rec.Revision, ds.Nodes[0].Blocked)
	}
	// raise quota -> active again
	got.QuotaBytes = 5000
	if err := m.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetShare(ctx, sh.ID)
	if got.Status != domain.ShareActive {
		t.Fatalf("expected active after quota raise, got %s", got.Status)
	}
	// period rollover resets usage
	*now = time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	got.QuotaBytes = 1000
	if err := st.UpdateShare(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetShare(ctx, sh.ID)
	if got.UsedUpload != 0 || got.UsedDownload != 0 || got.PeriodStart != time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("period not reset: %+v", got)
	}
	// expiry
	exp := now.Add(-time.Hour)
	got.ExpiresAt = &exp
	_ = st.UpdateShare(ctx, &got)
	_ = m.Tick(ctx)
	got, _ = st.GetShare(ctx, sh.ID)
	if got.Status != domain.ShareExpired {
		t.Fatalf("expected expired, got %s", got.Status)
	}
	// pause is sticky, resume re-evaluates (still expired)
	got.ExpiresAt = nil
	_ = st.UpdateShare(ctx, &got)
	if err := m.Pause(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	_ = m.Tick(ctx)
	got, _ = st.GetShare(ctx, sh.ID)
	if got.Status != domain.SharePaused {
		t.Fatalf("pause must be sticky, got %s", got.Status)
	}
	if err := m.Resume(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetShare(ctx, sh.ID)
	if got.Status != domain.ShareActive {
		t.Fatalf("expected active after resume, got %s", got.Status)
	}
	// remove a protocol target -> node revoked
	got.Targets = []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}
	if err := m.Update(ctx, &got); err != nil {
		t.Fatal(err)
	}
	live, _ := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if len(live) != 1 || live[0].Protocol != "vless" {
		t.Fatalf("expected only vless live, got %d", len(live))
	}
	// revoke and reissue
	if err := m.Revoke(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	live, _ = st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if len(live) != 0 {
		t.Fatalf("revoked share must have no live nodes")
	}
	sub, _ = st.GetSubscriptionByShare(ctx, sh.ID)
	if sub.Enabled {
		t.Fatal("subscription must be disabled after revoke")
	}
	newToken, err := m.Reissue(ctx, sh.ID)
	if err != nil || newToken == token {
		t.Fatalf("reissue: %v", err)
	}
	live, _ = st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if len(live) != 1 {
		t.Fatalf("reissue should restore nodes, got %d", len(live))
	}
	sub, _ = st.GetSubscriptionByShare(ctx, sh.ID)
	if !sub.Enabled || sub.TokenHint != newToken[:6] {
		t.Fatalf("subscription not reissued: %+v", sub)
	}
	if err := m.Delete(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetShare(ctx, sh.ID); err != store.ErrNotFound {
		t.Fatal("share should be deleted")
	}
	if _, err := st.GetSubscription(ctx, sub.ID); err != store.ErrNotFound {
		t.Fatal("subscription should cascade")
	}
}

func TestTrafficIngestDeltas(t *testing.T) {
	m, st, srv, now := setup(t)
	ctx := context.Background()
	sh := &domain.Share{Name: "bob", QuotaBytes: 0, Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}}
	if _, err := m.Create(ctx, sh); err != nil {
		t.Fatal(err)
	}
	nodes, _ := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	port := nodes[0].ListenPort
	ing := traffic.New(st)
	ing.Now = func() time.Time { return *now }
	hb := agentproto.Heartbeat{Epoch: "e1", TS: *now, Metrics: agentproto.Metrics{NetRx: 1000, NetTx: 2000}, Ports: []agentproto.PortCounter{{Port: port, Rx: 100, Tx: 200}}}
	res, err := ing.Ingest(ctx, srv, hb)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reset || len(res.Shares) != 0 {
		t.Fatalf("first heartbeat must only set baseline: %+v", res)
	}
	hb.TS = hb.TS.Add(time.Second)
	hb.Metrics.NetRx, hb.Metrics.NetTx = 1500, 2600
	hb.Ports[0].Rx, hb.Ports[0].Tx = 150, 260
	res, err = ing.Ingest(ctx, srv, hb)
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerUp != 500 || res.ServerDown != 600 || len(res.Shares) != 1 || res.Shares[0].Up != 50 || res.Shares[0].Down != 60 {
		t.Fatalf("deltas: %+v", res)
	}
	// epoch change with still-climbing NIC counters (agent restart): rebase only
	hb.Epoch = "e1.5"
	hb.TS = hb.TS.Add(time.Second)
	hb.Metrics.NetRx, hb.Metrics.NetTx = 1600, 2700
	hb.Ports[0].Rx, hb.Ports[0].Tx = 160, 270
	res, _ = ing.Ingest(ctx, srv, hb)
	if res.ServerUp != 0 || res.ServerDown != 0 || len(res.Shares) != 0 {
		t.Fatalf("epoch change without reset must not dump counters: %+v", res)
	}
	// epoch change after a real reset (reboot): count the new readings
	hb.Epoch = "e2"
	hb.TS = hb.TS.Add(time.Second)
	hb.Metrics.NetRx, hb.Metrics.NetTx = 10, 20
	hb.Ports[0].Rx, hb.Ports[0].Tx = 1, 2
	res, _ = ing.Ingest(ctx, srv, hb)
	if res.ServerUp != 10 || res.Shares[0].Up != 1 {
		t.Fatalf("epoch reset: %+v", res)
	}
	up, down, _ := st.SumTraffic(ctx, store.SubjectNode, nodes[0].ID, now.AddDate(0, 0, -1), *now)
	if up != 51 || down != 62 {
		t.Fatalf("node totals: %d %d", up, down)
	}
	if err := m.EvaluateDeltas(ctx, res.Shares); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetShare(ctx, sh.ID)
	if got.UsedUpload != 51 || got.UsedDownload != 62 {
		t.Fatalf("share usage: %+v", got)
	}
	series, err := ing.Daily(ctx, store.SubjectServer, srv.ID, 30)
	if err != nil || len(series.Points) != 30 || series.TotalUp != 510 {
		t.Fatalf("series: %v %d %d", err, len(series.Points), series.TotalUp)
	}
}

func TestPeriodStart(t *testing.T) {
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if ps := traffic.PeriodStart(now, 15); ps != time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC) {
		t.Fatal(ps)
	}
	if ps := traffic.PeriodStart(now, 10); ps != time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) {
		t.Fatal(ps)
	}
	if ps := traffic.PeriodStart(now, 1); ps != time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatal(ps)
	}
	if ps := traffic.PeriodStart(now, 31); ps != time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("last-day before Mar 31: %s", ps)
	}
	if ps := traffic.PeriodStart(time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC), 31); ps != time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("last-day on Mar 31: %s", ps)
	}
	if ps := traffic.PeriodStart(time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 31); ps != time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("last-day on Feb 28: %s", ps)
	}
	if nr := traffic.NextReset(time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), 31); nr != time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("next last-day after Jan 31: %s", nr)
	}
	if ps := traffic.PeriodStart(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), 30); ps != time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("29/30/31 are last day: %s", ps)
	}
	if ps := traffic.PeriodStart(time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), 29); ps != time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("29 in a 31-day month is last day: %s", ps)
	}
}

func TestShareDualBillingExhaustsAtHalf(t *testing.T) {
	m, st, srv, _ := setup(t)
	ctx := context.Background()
	sh := &domain.Share{Name: "dual", QuotaBytes: 1000, BillingMode: domain.BillingDual, ResetDay: 1,
		Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}}
	if _, err := m.Create(ctx, sh); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyDeltas(ctx, []traffic.ShareDelta{{ShareID: sh.ID, Up: 400, Down: 600}}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetShare(ctx, sh.ID)
	if got.Status != domain.ShareExhausted {
		t.Fatalf("400+600 two-way is 1000, want exhausted, got %s", got.Status)
	}
	u := m.UsageOf(got)
	if u.Used != 1000 || u.OneWay != 600 || u.TwoWay != 1000 {
		t.Fatalf("usage: %+v", u)
	}
}

func TestShareConnlogToggle(t *testing.T) {
	m, st, srv, _ := setup(t)
	ctx := context.Background()
	sh := &domain.Share{Name: "log", QuotaBytes: 0, BillingMode: domain.BillingDual, ResetDay: 1,
		Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}}
	if _, err := m.Create(ctx, sh); err != nil {
		t.Fatal(err)
	}
	if sh.ConnlogEnabled {
		t.Fatal("share connlog defaults off")
	}
	if err := m.SetConnlogEnabled(ctx, sh.ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetShare(ctx, sh.ID)
	if !got.ConnlogEnabled {
		t.Fatal("want enabled")
	}
	rec, err := st.LatestDesiredState(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := desired.Load(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !ds.Connlog.Enabled || len(ds.Nodes) == 0 || !ds.Nodes[0].ConnlogEnabled {
		t.Fatalf("desired connlog: %+v", ds)
	}
	if err := m.SetConnlogEnabled(ctx, sh.ID, false); err != nil {
		t.Fatal(err)
	}
	rec, _ = st.LatestDesiredState(ctx, srv.ID)
	ds, _ = desired.Load(rec)
	if ds.Nodes[0].ConnlogEnabled {
		t.Fatal("want node connlog off")
	}
}
