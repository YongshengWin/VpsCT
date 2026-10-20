package subscription

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
)

func applicationFixture(t *testing.T) (*store.Store, *Service, domain.Server, domain.Node) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "subscription.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sv := domain.Server{Name: "fixture", Enabled: true, PublicHost: "fixture.example.test", CoreMode: domain.CoreModeStable, CertMode: "self_signed"}
	if err := st.CreateServer(ctx, &sv); err != nil {
		t.Fatal(err)
	}
	n, err := provision.NewNode(sv, "", provision.Options{Protocol: "ss", Port: 21001})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	return st, NewService(st), sv, n
}

func acknowledgeApplication(t *testing.T, st *store.Store, sv domain.Server) domain.DesiredState {
	t.Helper()
	ctx := context.Background()
	rec, _, err := desired.New(st).Publish(ctx, sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := desired.Load(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := agentproto.ValidateDesired(ds, sv.ID, 0, ""); err != nil {
		t.Fatal("ordinary publication cannot be applied by the existing protocol", err)
	}
	if err := st.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", nil, 0); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestGeneratedSubscriptionRequiresAppliedProjectionAndKeepsOffline(t *testing.T) {
	st, svc, sv, n := applicationFixture(t)
	ctx := context.Background()
	sub := domain.Subscription{Kind: domain.SubGenerated, NodeSelection: domain.NodeSelection{IncludeAll: true}}
	assertCount := func(want int) {
		t.Helper()
		b, err := svc.Build(ctx, sub)
		if err != nil || len(b.Proxies) != want {
			t.Fatalf("proxy count=%d want=%d err=%v", len(b.Proxies), want, err)
		}
	}
	assertCount(0)
	first := acknowledgeApplication(t, st, sv)
	assertCount(1) // The agent has never heartbeated; its offline status is irrelevant.
	var params map[string]any
	if err := json.Unmarshal(n.Params, &params); err != nil {
		t.Fatal(err)
	}
	params["sni"] = "changed.example.test"
	n.Params, _ = json.Marshal(params)
	if err := st.UpdateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	assertCount(0)
	next := acknowledgeApplication(t, st, sv)
	if next.Revision <= first.Revision || next.Hash != first.Hash {
		t.Fatal("client-only parameter change reused the earlier receipt boundary")
	}
	assertCount(1)
	n.Revoked = true
	if err := st.UpdateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	assertCount(0)
}

func TestShareAuthorityImmediatelyOverridesPriorApplication(t *testing.T) {
	st, svc, sv, n := applicationFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	sh := domain.Share{Name: "fixture", Status: domain.ShareActive}
	if err := st.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	n.ShareID = &sh.ID
	if err := st.UpdateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	acknowledgeApplication(t, st, sv)
	for _, scenario := range []struct {
		name string
		edit func(*domain.Share)
	}{
		{"paused", func(s *domain.Share) { s.Status = domain.SharePaused }},
		{"revoked", func(s *domain.Share) { s.Status = domain.ShareRevoked }},
		{"expired before scheduler", func(s *domain.Share) { s.ExpiresAt = &now }},
		{"quota before scheduler", func(s *domain.Share) { s.QuotaBytes, s.UsedUpload, s.UsedDownload = 100, 60, 40 }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			changed := sh
			scenario.edit(&changed)
			if err := st.UpdateShare(ctx, &changed); err != nil {
				t.Fatal(err)
			}
			for _, sub := range []domain.Subscription{{Kind: domain.SubShare, ShareID: &sh.ID}, {Kind: domain.SubGenerated, NodeSelection: domain.NodeSelection{NodeIDs: []int64{n.ID}}}} {
				b, err := svc.Build(ctx, sub)
				if err != nil || len(b.Proxies) != 0 || len(b.Chains) != 0 {
					t.Fatal("old application bypassed current share authority", err)
				}
			}
		})
	}
}

func TestShareQuotaModesGateBeforeStateMachineRuns(t *testing.T) {
	for _, mode := range []string{domain.BillingDual, domain.BillingUp, domain.BillingDown, domain.BillingMax} {
		t.Run(mode, func(t *testing.T) {
			st, svc, server, node := applicationFixture(t)
			ctx := context.Background()
			sh := domain.Share{Name: "fixture", BillingMode: mode, QuotaBytes: 2000, Status: domain.ShareActive}
			if err := st.CreateShare(ctx, &sh); err != nil {
				t.Fatal(err)
			}
			node.ShareID = &sh.ID
			if err := st.UpdateNode(ctx, &node); err != nil {
				t.Fatal(err)
			}
			acknowledgeApplication(t, st, server)
			sh.UsedUpload, sh.UsedDownload, sh.QuotaBytes = 900, 600, 1000
			if err := st.UpdateShare(ctx, &sh); err != nil {
				t.Fatal(err)
			}
			assertGates := func(wantBlocked bool) {
				t.Helper()
				for _, sub := range []domain.Subscription{{Kind: domain.SubShare, ShareID: &sh.ID}, {Kind: domain.SubGenerated, NodeSelection: domain.NodeSelection{NodeIDs: []int64{node.ID}}}} {
					built, err := svc.Build(ctx, sub)
					if err != nil || (len(built.Proxies) == 0) != wantBlocked {
						t.Fatal("subscription gate used another quota mode", len(built.Proxies), wantBlocked, err)
					}
				}
				ds, err := desired.New(st).Build(ctx, server)
				if err != nil || len(ds.Nodes) != 1 || ds.Nodes[0].Blocked != wantBlocked {
					t.Fatal("agent gate used another quota mode", ds, wantBlocked, err)
				}
			}
			assertGates(mode == domain.BillingDual)
			sh.QuotaBytes = domain.QuotaBilled(mode, sh.UsedUpload, sh.UsedDownload)
			if err := st.UpdateShare(ctx, &sh); err != nil {
				t.Fatal(err)
			}
			assertGates(true)
		})
	}
}

func TestChainOriginCannotBypassCredentialRotationOrDeletion(t *testing.T) {
	st, svc, sv, landing := applicationFixture(t)
	ctx := context.Background()
	front := domain.Node{Name: "front", Protocol: "ss", Server: "front.example.test", Port: 21002, Params: json.RawMessage(`{"cipher":"aes-128-gcm","password":"fixture-front"}`), Source: domain.NodeManual, Enabled: true}
	if err := st.CreateNode(ctx, &front); err != nil {
		t.Fatal(err)
	}
	chain := landing
	chain.ID, chain.ServerID, chain.ShareID = 0, nil, nil
	chain.Name, chain.Source, chain.ChainFrontNodeID = "fixture-chain", domain.NodeChain, &front.ID
	if err := st.CreateChainNode(ctx, &chain, landing.ID); err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Kind: domain.SubGenerated, NodeSelection: domain.NodeSelection{NodeIDs: []int64{chain.ID}}}
	assert := func(want int) {
		t.Helper()
		b, err := svc.Build(ctx, sub)
		if err != nil || len(b.Chains) != want {
			t.Fatalf("chain count=%d want=%d err=%v", len(b.Chains), want, err)
		}
	}
	acknowledgeApplication(t, st, sv)
	assert(1)
	landing.Params = json.RawMessage(`{"cipher":"2022-blake3-aes-128-gcm","password":"fixture-rotated"}`)
	landing.ServerParams = json.RawMessage(`{"method":"2022-blake3-aes-128-gcm","password":"fixture-rotated"}`)
	if err := st.UpdateNodeCredentials(ctx, &landing); err != nil {
		t.Fatal(err)
	}
	assert(0)
	acknowledgeApplication(t, st, sv)
	assert(1)
	b, err := svc.Build(ctx, sub)
	if err != nil || b.Chains[0].Proxy.Params["password"] != "fixture-rotated" {
		t.Fatal("virtual chain resurrected copied old credentials", err)
	}
	if err := st.DeleteNode(ctx, landing.ID); err != nil {
		t.Fatal(err)
	}
	assert(0)
}
