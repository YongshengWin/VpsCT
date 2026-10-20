package share

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/store"
)

func TestNATShareFailureRollsBackNodesShareAndSubscription(t *testing.T) {
	m, st, srv, _ := setup(t)
	ctx := context.Background()
	a, _ := st.GetAgentByServer(ctx, srv.ID)
	if err := st.SetAgentEnrollToken(ctx, srv.ID, "fixture-enroll", st.Now().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteEnrollment(ctx, a.ID, "fixture-enroll", "fixture-token", "test"); err != nil {
		t.Fatal(err)
	}
	cfg := &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001, Count: 2, Network: "both"}}}
	d := agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: true, NetworkConfigureAllowed: true, NAT: &agentproto.NATStatus{Version: 2, Source: "local", Status: "applied", Configured: true, Config: cfg}}
	b, _ := json.Marshal(d)
	if err := st.Heartbeat(ctx, a.ID, "test", "", "", nil, b); err != nil {
		t.Fatal(err)
	}
	// A second insert failure stands in for a reservation/authority change after
	// preflight. The first insert and all share/subscription rows must disappear.
	if _, err := st.DB().Exec(`CREATE TRIGGER fixture_second_node BEFORE INSERT ON nodes WHEN NEW.protocol='trojan' BEGIN SELECT RAISE(ABORT,'fixture capacity changed'); END`); err != nil {
		t.Fatal(err)
	}
	sh := &domain.Share{Name: "NAT share", Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless", "trojan"}}}}
	if _, err := m.Create(ctx, sh); err == nil {
		t.Fatal("partial creation succeeded")
	}
	for _, table := range []string{"shares", "subscriptions", "nodes"} {
		var count int
		if err := st.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
	if sh.ID != 0 {
		t.Fatal("failed new share retained an id")
	}
	if _, err := st.LatestDesiredState(ctx, srv.ID); err != store.ErrNotFound {
		t.Fatal("failed allocation published partial desired", err)
	}
}

func TestShareUpdateDatabaseFailurePreservesPreviousTargetsAndNodes(t *testing.T) {
	m, st, srv, _ := setup(t)
	ctx := context.Background()
	sh := &domain.Share{Name: "atomic update", Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}}
	if _, err := m.Create(ctx, sh); err != nil {
		t.Fatal(err)
	}
	nodes, _ := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if len(nodes) != 1 {
		t.Fatal("missing fixture")
	}
	badOwner := int64(987654321)
	sh.UserID = &badOwner
	sh.Targets = nil
	if err := m.Update(ctx, sh); err == nil {
		t.Fatal("foreign-key failure unexpectedly passed")
	}
	saved, _ := st.GetShare(ctx, sh.ID)
	node, _ := st.GetNode(ctx, nodes[0].ID)
	if len(saved.Targets) != 1 || saved.UserID != nil || !node.Enabled || node.Revoked {
		t.Fatal("failed update revoked old target", saved.Targets, node.Enabled, node.Revoked)
	}
}

func TestShareRepeatedProtocolRejectedBeforeAllocation(t *testing.T) {
	m, st, srv, _ := setup(t)
	sh := &domain.Share{Name: "duplicate", Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless", "vless"}}}}
	if _, err := m.Create(context.Background(), sh); err == nil {
		t.Fatal("repeated protocol accepted")
	}
	for _, table := range []string{"shares", "subscriptions", "nodes"} {
		var count int
		if err := st.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
}
