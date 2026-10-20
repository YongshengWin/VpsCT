package share

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"ctlvps/internal/traffic"
	"testing"
	"time"
)

func TestDedicatedNodeQuotas(t *testing.T) {
	for _, mode := range []string{"dual", "up", "down", "max"} {
		t.Run(mode, func(t *testing.T) {
			m, st, server, now := setup(t)
			ctx := context.Background()
			sh := domain.Share{Name: "entry-quotas", BillingMode: mode, ResetDay: 1, Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss", "vless"}, QuotaBytes: map[string]int64{"ss": 100}}}}
			if _, err := m.Create(ctx, &sh); err != nil {
				t.Fatal(err)
			}
			acknowledgeShareNodes(t, st, server.ID)
			nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
			if err != nil {
				t.Fatal(err)
			}
			var limited domain.Node
			for _, n := range nodes {
				if n.Protocol == "ss" {
					limited = n
				}
			}
			ing := traffic.New(st)
			ing.Now = m.Now
			hb := agentproto.Heartbeat{TS: *now, Epoch: "quota-boot", Ports: []agentproto.PortCounter{{NodeID: limited.ID, Port: limited.ListenPort, Source: "nft-node-v1", Epoch: "quota-meter", FromZero: true, Rx: 100, Tx: 100}}}
			before, err := st.NetworkGeneration(ctx, server.ID)
			if err != nil {
				t.Fatal(err)
			}
			hb.Ports[0].Rx, hb.Ports[0].Tx = 10, 10
			below, err := ing.Ingest(ctx, server, hb)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.EvaluateDeltas(ctx, below.Shares); err != nil {
				t.Fatal(err)
			}
			unchanged, err := st.NetworkGeneration(ctx, server.ID)
			if err != nil || unchanged != before {
				t.Fatal("below-limit sample changed network generation", before, unchanged, err)
			}
			hb.Ports[0].Rx, hb.Ports[0].Tx = 100, 100
			result, err := ing.Ingest(ctx, server, hb)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.EvaluateDeltas(ctx, result.Shares); err != nil {
				t.Fatal(err)
			}
			check := func(blocked bool) {
				t.Helper()
				got, err := st.GetShare(ctx, sh.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != domain.ShareActive || got.NodeExhausted(limited) != blocked {
					t.Fatal("entry limit changed entire share", got.Status, got.NodeUsage)
				}
				rec, err := st.LatestDesiredState(ctx, server.ID)
				if err != nil {
					t.Fatal(err)
				}
				ds, err := desired.Load(rec)
				if err != nil {
					t.Fatal(err)
				}
				for _, n := range ds.Nodes {
					if n.Blocked != (n.NodeID == limited.ID && blocked) {
						t.Fatal("wrong entry blocked", n.NodeID, n.Blocked)
					}
				}
				acknowledgeShareNodes(t, st, server.ID)
				sub, err := st.GetSubscriptionByShare(ctx, sh.ID)
				if err != nil {
					t.Fatal(err)
				}
				svc := subscription.NewService(st)
				svc.Now = m.Now
				built, err := svc.Build(ctx, sub)
				if err != nil {
					t.Fatal(err)
				}
				want := 2
				if blocked {
					want = 1
				}
				if len(built.Proxies) != want {
					t.Fatal("subscription entries", len(built.Proxies), want)
				}
			}
			check(true)
			after, err := st.NetworkGeneration(ctx, server.ID)
			if err != nil || after <= before {
				t.Fatal("quota crossing did not fence stale publication", before, after, err)
			}
			// A management snapshot from before ingestion cannot overwrite entry counters.
			if err := m.Update(ctx, &sh); err != nil {
				t.Fatal(err)
			}
			check(true)
			if _, err := m.Reissue(ctx, sh.ID); err != nil {
				t.Fatal(err)
			}
			check(true)
			// Duplicate durable counters cannot charge the entry twice.
			if _, err := ing.Ingest(ctx, server, hb); err != nil {
				t.Fatal(err)
			}
			got, err := st.GetShare(ctx, sh.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.NodeUsage[limited.ID] != [2]int64{100, 100} {
				t.Fatal("entry double charged", got.NodeUsage)
			}
			// Stale settings must preserve accounting; raising/removing limits restores only this entry.
			got.Targets[0].QuotaBytes["ss"] = 1000
			if err := m.Update(ctx, &got); err != nil {
				t.Fatal(err)
			}
			check(false)
			got.Targets[0].QuotaBytes["ss"] = 100
			if err := m.Update(ctx, &got); err != nil {
				t.Fatal(err)
			}
			check(true)
			if err := m.ResetUsage(ctx, sh.ID); err != nil {
				t.Fatal(err)
			}
			check(false)
			hb.Ports[0].Rx = 200
			hb.Ports[0].Tx = 200
			result, err = ing.Ingest(ctx, server, hb)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.EvaluateDeltas(ctx, result.Shares); err != nil {
				t.Fatal(err)
			}
			check(true)
			*now = now.AddDate(0, 1, 0)
			if err := m.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			check(false)
			// Delayed reports from the previous period never consume the new entry quota.
			hb.Ports[0].Rx = 300
			hb.Ports[0].Tx = 300
			hb.TS = now.Add(-40 * 24 * time.Hour)
			result, err = ing.Ingest(ctx, server, hb)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.EvaluateDeltas(ctx, result.Shares); err != nil {
				t.Fatal(err)
			}
			check(false)
		})
	}
}
