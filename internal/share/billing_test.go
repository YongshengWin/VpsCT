package share

import (
	"context"
	"math"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"ctlvps/internal/traffic"
)

func acknowledgeShareNodes(t *testing.T, st *store.Store, serverID int64) {
	t.Helper()
	ctx := context.Background()
	rec, err := st.LatestDesiredState(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordNodeApplications(ctx, serverID, rec.Revision, rec.Hash, "applied", nil, 0); err != nil {
		t.Fatal(err)
	}
}

func TestShareBillingModesAcrossServersAgreeWithHeaderAndGates(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		billed int64
	}{
		{domain.BillingDual, 1500},
		{domain.BillingUp, 900},
		{domain.BillingDown, 600},
		{domain.BillingMax, 900},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			m, st, first, now := setup(t)
			ctx := context.Background()
			// Server modes deliberately differ from the share's independent mode.
			first.QuotaBilling = domain.BillingDown
			if err := st.UpdateServer(ctx, &first); err != nil {
				t.Fatal(err)
			}
			second := domain.Server{Name: "second-fixture", PublicHost: "fixture.example.test", Enabled: true, CoreMode: domain.CoreModeStable, QuotaBilling: domain.BillingUp}
			if err := st.CreateServer(ctx, &second); err != nil {
				t.Fatal(err)
			}
			sh := domain.Share{Name: "billing-fixture", QuotaBytes: 1000, BillingMode: tc.mode, Targets: []domain.ShareTarget{
				{ServerID: first.ID, Protocols: []string{"ss"}},
				{ServerID: second.ID, Protocols: []string{"ss"}},
			}}
			if _, err := m.Create(ctx, &sh); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{first.ID, second.ID} {
				acknowledgeShareNodes(t, st, id)
			}
			nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
			if err != nil || len(nodes) != 2 {
				t.Fatal("share nodes", len(nodes), err)
			}
			ing := traffic.New(st)
			ing.Now = m.Now
			for _, node := range nodes {
				server := first
				rx, tx := int64(800), int64(100)
				if *node.ServerID == second.ID {
					server, rx, tx = second, 100, 500
				}
				hb := agentproto.Heartbeat{TS: *now, Epoch: "fixture-boot", Ports: []agentproto.PortCounter{{NodeID: node.ID, Port: node.ListenPort, Source: "nft-node-v1", Epoch: "fixture-meter", FromZero: true, Rx: rx, Tx: tx}}}
				result, err := ing.Ingest(ctx, server, hb)
				if err != nil {
					t.Fatal(err)
				}
				if err := m.EvaluateDeltas(ctx, result.Shares); err != nil {
					t.Fatal(err)
				}
				// A retry must not change any raw or billed total.
				result, err = ing.Ingest(ctx, server, hb)
				if err != nil || len(result.Shares) != 0 {
					t.Fatal("duplicate heartbeat charged again", result.Shares, err)
				}
			}
			got, err := st.GetShare(ctx, sh.ID)
			if err != nil || got.UsedUpload != 900 || got.UsedDownload != 600 {
				t.Fatal("cross-server raw aggregation", got, err)
			}
			usage := m.UsageOf(got)
			wantRemaining := max(int64(0), int64(1000)-tc.billed)
			if usage.BillingMode != tc.mode || usage.Used != tc.billed || usage.Billed != tc.billed || usage.Inbound != 900 || usage.Outbound != 600 || usage.Total != 1500 || usage.Remaining != wantRemaining || math.Abs(usage.Percent-float64(tc.billed)/10) > 0.000001 {
				t.Fatal("display disagrees with quota/raw totals", usage)
			}
			svc := subscription.NewService(st)
			svc.Now = m.Now
			sub, err := st.GetSubscriptionByShare(ctx, sh.ID)
			if err != nil {
				t.Fatal(err)
			}
			built, err := svc.Build(ctx, sub)
			if err != nil || built.Userinfo == nil || built.Userinfo.Upload+built.Userinfo.Download != tc.billed || built.Userinfo.Remaining() != usage.Remaining {
				t.Fatal("subscription header disagrees with display", built, err)
			}
			wantStatus, wantProxies := domain.ShareActive, 2
			if tc.billed >= 1000 {
				wantStatus, wantProxies = domain.ShareExhausted, 0
			}
			if got.Status != wantStatus || len(built.Proxies) != wantProxies {
				t.Fatal("quota gates disagree with billing", got.Status, len(built.Proxies))
			}
			for _, id := range []int64{first.ID, second.ID} {
				rec, err := st.LatestDesiredState(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				ds, err := desired.Load(rec)
				if err != nil || len(ds.Nodes) != 1 || ds.Nodes[0].Blocked != (wantStatus == domain.ShareExhausted) {
					t.Fatal("agent desired quota gate disagrees", ds, err)
				}
			}
			// Equality exhausts every mode; removing the quota reactivates it.
			got.QuotaBytes = tc.billed
			if err := m.Update(ctx, &got); err != nil || got.Status != domain.ShareExhausted {
				t.Fatal("exact quota did not exhaust", got.Status, err)
			}
			got.QuotaBytes = 0
			if err := m.Update(ctx, &got); err != nil || got.Status != domain.ShareActive || m.UsageOf(got).Remaining != -1 {
				t.Fatal("unlimited quota did not reactivate", got.Status, err)
			}
			if got.UsedUpload != 900 || got.UsedDownload != 600 {
				t.Fatal("editing quota rewrote raw counters")
			}
		})
	}
}

func TestShareEditCannotOverwriteHeartbeatBetweenReadAndSave(t *testing.T) {
	m, st, server, now := setup(t)
	ctx := context.Background()
	sh := domain.Share{Name: "fixture", BillingMode: domain.BillingDual, QuotaBytes: 500, Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	if _, err := m.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil || len(nodes) != 1 {
		t.Fatal("share node", err)
	}
	ing := traffic.New(st)
	ing.Now = m.Now
	hb := agentproto.Heartbeat{TS: *now, Epoch: "fixture-boot", Ports: []agentproto.PortCounter{{NodeID: nodes[0].ID, Source: "nft-node-v1", Epoch: "fixture-meter", FromZero: true, Rx: 300, Tx: 400}}}
	var ingestionErr error
	ingested := false
	// Update reads the share before asking the store for its write timestamp.
	// At that exact read/save boundary commit a real metering transaction.
	st.Now = func() time.Time {
		if !ingested {
			ingested = true
			_, ingestionErr = ing.Ingest(ctx, server, hb)
		}
		return *now
	}
	sh.Name, sh.BillingMode = "edited", domain.BillingMax
	if err := m.Update(ctx, &sh); err != nil || ingestionErr != nil || !ingested {
		t.Fatal("interleaved edit/ingest", err, ingestionErr, ingested)
	}
	if sh.UsedUpload != 300 || sh.UsedDownload != 400 || sh.Status != domain.ShareActive || m.UsageOf(sh).Used != 400 {
		t.Fatal("settings update lost committed traffic or used a stale quota decision", sh)
	}
}

func TestShareModeChangeReevaluatesWholePeriodAndKeepsManualPause(t *testing.T) {
	m, st, server, _ := setup(t)
	ctx := context.Background()
	sh := domain.Share{Name: "fixture", BillingMode: domain.BillingDual, QuotaBytes: 1000, Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	if _, err := m.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyDeltas(ctx, []traffic.ShareDelta{{ShareID: sh.ID, Up: 900, Down: 600}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.Status != domain.ShareExhausted {
		t.Fatal("dual quota not exhausted", got.Status, err)
	}
	got.BillingMode = domain.BillingMax
	if err := m.Update(ctx, &got); err != nil || got.Status != domain.ShareActive || m.UsageOf(got).Billed != 900 || got.UsedUpload != 900 || got.UsedDownload != 600 {
		t.Fatal("changing mode did not re-evaluate the same raw period", got, err)
	}
	if err := m.Pause(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	// This stale edit still carries active; settings must preserve manual pause.
	got.BillingMode = domain.BillingDown
	if err := m.Update(ctx, &got); err != nil || got.Status != domain.SharePaused || m.UsageOf(got).Billed != 600 {
		t.Fatal("changing mode undid a manual pause", got.Status, err)
	}
}

func TestShareTickCannotResetNewPeriodHeartbeatUsage(t *testing.T) {
	m, st, server, now := setup(t)
	ctx := context.Background()
	sh := domain.Share{Name: "fixture", ResetDay: 1, QuotaBytes: 1000, Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	if _, err := m.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil || len(nodes) != 1 {
		t.Fatal("share node", err)
	}
	*now = time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	ing := traffic.New(st)
	ing.Now = m.Now
	hb := agentproto.Heartbeat{TS: *now, Epoch: "fixture-boot", Ports: []agentproto.PortCounter{{NodeID: nodes[0].ID, Source: "nft-node-v1", Epoch: "fixture-meter", FromZero: true, Rx: 300, Tx: 400}}}
	var ingestionErr error
	ingested := false
	// Tick has read March's period before the reset's write timestamp is read.
	// Ingestion atomically rolls over April and commits usage at that boundary.
	st.Now = func() time.Time {
		if !ingested {
			ingested = true
			_, ingestionErr = ing.Ingest(ctx, server, hb)
		}
		return *now
	}
	if err := m.Tick(ctx); err != nil || ingestionErr != nil || !ingested {
		t.Fatal("interleaved rollover", err, ingestionErr, ingested)
	}
	got, err := st.GetShare(ctx, sh.ID)
	wantPeriod := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if err != nil || !got.PeriodStart.Equal(wantPeriod) || got.UsedUpload != 300 || got.UsedDownload != 400 || m.UsageOf(got).Billed != 700 {
		t.Fatal("Tick erased the heartbeat's new period usage", got, err)
	}
}

func TestShareAutomaticStatusCannotOverwriteManualPause(t *testing.T) {
	m, st, server, now := setup(t)
	ctx := context.Background()
	sh := domain.Share{Name: "fixture", QuotaBytes: 1000, Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	if _, err := m.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AddShareUsage(ctx, sh.ID, 900, 600); err != nil {
		t.Fatal(err)
	}
	var pauseErr error
	paused := false
	// The automatic transition has read active/1500 before its status write.
	st.Now = func() time.Time {
		if !paused {
			paused = true
			pauseErr = m.Pause(ctx, sh.ID)
		}
		return *now
	}
	if err := m.Tick(ctx); err != nil || pauseErr != nil || !paused {
		t.Fatal("interleaved pause", err, pauseErr, paused)
	}
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.Status != domain.SharePaused || got.UsedUpload != 900 || got.UsedDownload != 600 {
		t.Fatal("automatic evaluation undid the pause", got.Status, err)
	}
}

func TestShareAutomaticStatusReevaluatesNewHeartbeatTotals(t *testing.T) {
	m, st, server, now := setup(t)
	ctx := context.Background()
	sh := domain.Share{Name: "fixture", QuotaBytes: 1000, Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	if _, err := m.Create(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShareStatus(ctx, sh.ID, domain.ShareExhausted); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil || len(nodes) != 1 {
		t.Fatal("share node", err)
	}
	ing := traffic.New(st)
	ing.Now = m.Now
	hb := agentproto.Heartbeat{TS: *now, Epoch: "fixture-boot", Ports: []agentproto.PortCounter{{NodeID: nodes[0].ID, Source: "nft-node-v1", Epoch: "fixture-meter", FromZero: true, Rx: 900, Tx: 600}}}
	var ingestionErr error
	ingested := false
	// Tick plans to reactivate exhausted/0. A heartbeat then consumes 1500;
	// its baseline and charge must survive and the stale activation must lose.
	st.Now = func() time.Time {
		if !ingested {
			ingested = true
			_, ingestionErr = ing.Ingest(ctx, server, hb)
		}
		return *now
	}
	if err := m.Tick(ctx); err != nil || ingestionErr != nil || !ingested {
		t.Fatal("interleaved quota evaluation", err, ingestionErr, ingested)
	}
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.Status != domain.ShareExhausted || got.UsedUpload != 900 || got.UsedDownload != 600 {
		t.Fatal("old zero-count evaluation reactivated newly exhausted usage", got, err)
	}
}
