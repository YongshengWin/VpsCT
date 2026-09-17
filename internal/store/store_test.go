package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateAndUsers(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := &domain.User{Username: "admin", PasswordHash: "x", Role: domain.RoleAdmin, Enabled: true}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUserByName(ctx, "admin")
	if err != nil || got.ID != u.ID || got.Role != domain.RoleAdmin {
		t.Fatalf("get user: %v %+v", err, got)
	}
	if _, err := s.GetUser(ctx, 999); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
	sess := Session{ID: "abc", UserID: u.ID, CreatedAt: s.Now(), ExpiresAt: s.Now().Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }
	if _, err := s.GetSession(ctx, "abc"); err != ErrNotFound {
		t.Fatalf("expected expired session, got %v", err)
	}
}

func TestServerNodeShareFlow(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	srv := &domain.Server{Name: "hk-1", Region: "HK", PublicHost: "1.2.3.4", Enabled: true}
	if err := s.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgentByServer(ctx, srv.ID); err != nil {
		t.Fatalf("agent row should exist: %v", err)
	}
	n := &domain.Node{Name: "hk-reality", Protocol: "vless", Server: "1.2.3.4", Port: 443, Params: []byte(`{"uuid":"u"}`), ServerID: &srv.ID, ListenPort: 443, Core: domain.CoreSingBox, Enabled: true, Source: domain.NodeDeployed}
	if err := s.CreateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes(ctx, NodeFilter{ServerID: &srv.ID})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("list nodes: %v %d", err, len(nodes))
	}
	ports, _ := s.UsedListenPorts(ctx, srv.ID)
	if !ports[443] {
		t.Fatal("port 443 should be used")
	}
	sh := &domain.Share{Name: "alice", QuotaBytes: 100, Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"vless"}}}}
	if err := s.CreateShare(ctx, sh); err != nil {
		t.Fatal(err)
	}
	up, down, err := s.AddShareUsage(ctx, sh.ID, 30, 40)
	if err != nil || up != 30 || down != 40 {
		t.Fatalf("usage: %v %d %d", err, up, down)
	}
	if err := s.AddTraffic(ctx, SubjectShare, sh.ID, s.Now(), 30, 40); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTraffic(ctx, SubjectShare, sh.ID, s.Now(), 1, 1); err != nil {
		t.Fatal(err)
	}
	u2, d2, err := s.SumTraffic(ctx, SubjectShare, sh.ID, s.Now().Add(-24*time.Hour), s.Now())
	if err != nil || u2 != 31 || d2 != 41 {
		t.Fatalf("sum: %v %d %d", err, u2, d2)
	}
	ds, err := s.CreateDesiredState(ctx, srv.ID, []byte(`{"a":1}`), "h1")
	if err != nil || ds.Revision != 1 {
		t.Fatalf("desired: %v %+v", err, ds)
	}
	ds2, _ := s.CreateDesiredState(ctx, srv.ID, []byte(`{"a":2}`), "h2")
	if ds2.Revision != 2 {
		t.Fatalf("revision should increment: %d", ds2.Revision)
	}
	list, _ := s.ListDesiredStates(ctx, srv.ID, 10)
	if list[1].Status != domain.DesiredStale {
		t.Fatalf("old revision should be superseded: %s", list[1].Status)
	}
	if err := s.DeleteServer(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNode(ctx, n.ID); err != ErrNotFound {
		t.Fatalf("node should be deleted with server: %v", err)
	}
}

func TestReplaceExternalNodes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ext := &domain.ExternalSubscription{Name: "airport", URL: "https://x/sub", Enabled: true}
	if err := s.CreateExternal(ctx, ext); err != nil {
		t.Fatal(err)
	}
	first := []domain.Node{{Name: "a", Protocol: "ss"}, {Name: "b", Protocol: "ss"}}
	added, updated, removed, err := s.ReplaceExternalNodes(ctx, ext.ID, first)
	if err != nil || added != 2 || updated != 0 || removed != 0 {
		t.Fatalf("first sync: %v %d %d %d", err, added, updated, removed)
	}
	nodes, _ := s.ListNodes(ctx, NodeFilter{ExternalSubID: &ext.ID})
	idA := nodes[0].ID
	second := []domain.Node{{Name: "a", Protocol: "vmess"}, {Name: "c", Protocol: "ss"}}
	added, updated, removed, err = s.ReplaceExternalNodes(ctx, ext.ID, second)
	if err != nil || added != 1 || updated != 1 || removed != 1 {
		t.Fatalf("second sync: %v %d %d %d", err, added, updated, removed)
	}
	a, _ := s.GetNode(ctx, idA)
	if a.Protocol != "vmess" {
		t.Fatalf("node a should keep id and update protocol: %+v", a)
	}
	e, _ := s.GetExternal(ctx, ext.ID)
	if e.NodeCount != 2 {
		t.Fatalf("node_count: %d", e.NodeCount)
	}
}

func TestSettings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if v := s.GetSettingInt(ctx, "x", 7); v != 7 {
		t.Fatal("default")
	}
	_ = s.SetSetting(ctx, "x", "9")
	if v := s.GetSettingInt(ctx, "x", 7); v != 9 {
		t.Fatal("set")
	}
}

func TestDeleteServerRemovesAssociatedNodesAndChains(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server := domain.Server{Name: "deleted"}
	other := domain.Server{Name: "retained"}
	for _, srv := range []*domain.Server{&server, &other} {
		if err := s.CreateServer(ctx, srv); err != nil {
			t.Fatal(err)
		}
	}
	add := func(n domain.Node) domain.Node {
		t.Helper()
		if err := s.CreateNode(ctx, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	deployed := add(domain.Node{Name: "deployed", Source: domain.NodeDeployed, ServerID: &server.ID, Server: "deleted.example", Port: 443, Protocol: "vless"})
	revoked := add(domain.Node{Name: "revoked", Source: domain.NodeDeployed, ServerID: &server.ID, Revoked: true})
	bound := add(domain.Node{Name: "bound", Source: domain.NodeManual, ServerID: &server.ID})
	retained := add(domain.Node{Name: "retained", Source: domain.NodeDeployed, ServerID: &other.ID, Server: "retained.example", Port: 443, Protocol: "vless"})
	manual := add(domain.Node{Name: "manual", Source: domain.NodeManual, Server: deployed.Server, Port: deployed.Port, Protocol: deployed.Protocol})
	frontChain := add(domain.Node{Name: "front chain", Source: domain.NodeChain, ChainFrontNodeID: &deployed.ID, Server: retained.Server, Port: retained.Port, Protocol: retained.Protocol})
	landingChain := add(domain.Node{Name: "landing chain", Source: domain.NodeChain, ChainFrontNodeID: &retained.ID, Server: deployed.Server, Port: deployed.Port, Protocol: deployed.Protocol})
	safeChain := add(domain.Node{Name: "safe chain", Source: domain.NodeChain, ChainFrontNodeID: &retained.ID, Server: "independent.example", Port: 443, Protocol: "vless"})
	if err := s.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	for _, n := range []domain.Node{deployed, revoked, bound, frontChain, landingChain} {
		if _, err := s.GetNode(ctx, n.ID); err != ErrNotFound {
			t.Fatalf("%s should be deleted: %v", n.Name, err)
		}
	}
	for _, n := range []domain.Node{retained, manual, safeChain} {
		if _, err := s.GetNode(ctx, n.ID); err != nil {
			t.Fatalf("%s should survive: %v", n.Name, err)
		}
	}
	if _, err := s.GetAgentByServer(ctx, server.ID); err != ErrNotFound {
		t.Fatalf("agent should be deleted: %v", err)
	}
	if _, err := s.GetServer(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
}
