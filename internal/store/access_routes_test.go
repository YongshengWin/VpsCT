package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func accessRouteFixture(t *testing.T, s *Store) AccessRouteRequest {
	t.Helper()
	ctx := context.Background()
	ids := []int64{}
	var landing domain.Node
	for i := 0; i < 3; i++ {
		nr, diag, _ := readyNetworkFixture(t, s)
		n, _ := s.GetNode(ctx, nr.NodeID)
		diag.NetworkForwardVersion = 1
		diag.ForwardPrivateVersion = 1
		diag.ForwardUDPVersion = 1
		writeNetworkDiagnostics(t, s, *n.ServerID, diag)
		ids = append(ids, *n.ServerID)
		landing = n
	}
	// Only documentation ranges, never a local target or historical server address.
	in := AccessRouteRequest{ID: strings.Repeat("d", 32), Name: "test route", EntryServerID: ids[0], IXServerID: ids[1], LandingNodeID: landing.ID, EntryPort: 24501, IXPort: 24502, AdvertiseHost: "entry.example.test", IXHost: "203.0.113.20", LandingHost: "203.0.113.30", Network: "tcp", SourceMode: "all", SourceCIDRs: []string{}}
	return in
}
func createTestRoute(t *testing.T, s *Store, in AccessRouteRequest) AccessRoute {
	t.Helper()
	ctx := context.Background()
	v, err := s.PreviewAccessRoute(ctx, in)
	if err != nil || !v.Ready {
		t.Fatalf("preview: ready=%v err=%v checks=%+v / %+v", v.Ready, err, v.Entry.Checks, v.IX.Checks)
	}
	in = v.Candidate
	in.ExpectedImpact = v.Token
	r, err := s.CreateAccessRoute(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func applyRouteSource(t *testing.T, s *Store, r AccessRoute) {
	t.Helper()
	ctx := context.Background()
	n, err := s.GetNode(ctx, r.Request.LandingNodeID)
	if err != nil {
		t.Fatal(err)
	}
	sv, _ := s.GetServer(ctx, *n.ServerID)
	rec := applicationPublish(t, s, sv, []domain.Node{n}, "a")
	if err = s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", nil, 0); err != nil {
		t.Fatal(err)
	}
}
func acknowledgeRouteServer(t *testing.T, s *Store, id int64) {
	t.Helper()
	ctx := context.Background()
	g, err := s.NetworkGeneration(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := s.LatestDesiredState(ctx, id)
	wanted := &agentproto.DesiredState{ServerID: id, NetworkGeneration: g, Hash: strings.Repeat("a", 64)}
	rec, _, err := s.PublishDesired(ctx, latest.Revision, wanted, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE agents SET applied_revision=?,applied_hash=?,apply_error='' WHERE server_id=?`, rec.Revision, rec.Hash, id); err != nil {
		t.Fatal(err)
	}
}
func advanceRoute(t *testing.T, s *Store, r AccessRoute) AccessRoute {
	t.Helper()
	if err := s.ReconcileAccessRoute(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.AccessRoute(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAccessRouteCreationIsAtomicReviewedAndIdempotent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := accessRouteFixture(t, s)
	v, err := s.PreviewAccessRoute(ctx, in)
	if err != nil || !v.Ready {
		t.Fatal(v, err)
	}
	in = v.Candidate
	in.ExpectedImpact = v.Token
	if _, err = s.db.Exec(`CREATE TRIGGER fail_route_audit BEFORE INSERT ON audit_log WHEN NEW.action='route.create' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateAccessRoute(ctx, in, domain.AuditEvent{}); err == nil {
		t.Fatal("partial request survived audit failure")
	}
	for _, table := range []string{"access_routes", "port_forwards", "subscriptions"} {
		var n int
		if err = s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	s.db.Exec(`DROP TRIGGER fail_route_audit`)
	r, err := s.CreateAccessRoute(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := s.CreateAccessRoute(ctx, in, domain.AuditEvent{})
	if err != nil || repeat.ID != r.ID || repeat.EntryForwardID != r.EntryForwardID {
		t.Fatal("retry duplicated route", repeat, err)
	}
	in.Name = "changed"
	if _, err = s.CreateAccessRoute(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkOperationConflict) {
		t.Fatal("id reused", err)
	}
	for _, id := range []int64{r.EntryForwardID, r.IXForwardID} {
		f, _ := s.GetPortForward(ctx, id)
		if f.Enabled {
			t.Fatal("unreviewed early listener")
		}
		cfg := f.Config
		_, err = s.RequestPortForward(ctx, PortForwardRequest{ID: strings.Repeat("f", 32), Action: "update", ForwardID: id, ExpectedRevision: f.Revision, Name: f.Name, Enabled: true, Config: &cfg}, domain.AuditEvent{})
		if !errors.Is(err, ErrRouteOwned) {
			t.Fatal("segment escaped route ownership", err)
		}
	}
	if err = s.DeleteNode(ctx, r.Request.LandingNodeID); !errors.Is(err, ErrAccessRouteInUse) {
		t.Fatal("active source deleted", err)
	}
	if err = s.DeleteServer(ctx, r.Request.IXServerID); !errors.Is(err, ErrAccessRouteInUse) {
		t.Fatal("active IX deleted", err)
	}
}
func TestAccessRouteRechecksPreviewAndRejectsUnsupportedSource(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := accessRouteFixture(t, s)
	v, err := s.PreviewAccessRoute(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in = v.Candidate
	in.ExpectedImpact = v.Token
	f := forwardFixture(in.EntryServerID, in.EntryPort)
	if _, err = s.RequestPortForward(ctx, PortForwardRequest{ID: strings.Repeat("c", 32), Action: "create", ServerID: f.ServerID, Name: f.Name, Enabled: false, Config: &f.Config}, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateAccessRoute(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactChanged) {
		t.Fatal("stale plan accepted", err)
	}
	in.ID = strings.Repeat("b", 32)
	in.EntryPort = 24510
	in.LandingPort++
	if _, err = s.PreviewAccessRoute(ctx, in); !errors.Is(err, ErrAccessRoute) {
		t.Fatal("unrelated target port accepted", err)
	}
}
func TestAccessRouteOrdersApplicationAndGatesStaleCredentials(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	r := createTestRoute(t, s, accessRouteFixture(t, s))
	r = advanceRoute(t, s, r)
	if r.Stage != "blocked" {
		t.Fatal(r.Stage)
	}
	if _, _, ok, err := s.AccessRouteLanding(ctx, r.ID); err != nil || ok {
		t.Fatal("unapplied route visible", err)
	}
	applyRouteSource(t, s, r)
	r = advanceRoute(t, s, r)
	if r.Stage != "pending_ix" {
		t.Fatal(r.Stage)
	}
	ef, _ := s.GetPortForward(ctx, r.EntryForwardID)
	xf, _ := s.GetPortForward(ctx, r.IXForwardID)
	if ef.Enabled || !xf.Enabled {
		t.Fatal("entry enabled before IX")
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "pending_ix" {
		t.Fatal("publication mistaken for application", r.Stage)
	}
	acknowledgeRouteServer(t, s, r.Request.IXServerID)
	r = advanceRoute(t, s, r)
	if r.Stage != "pending_entry" {
		t.Fatal(r.Stage)
	}
	if _, _, ok, _ := s.AccessRouteLanding(ctx, r.ID); ok {
		t.Fatal("entry receipt missing")
	}
	acknowledgeRouteServer(t, s, r.Request.EntryServerID)
	r = advanceRoute(t, s, r)
	if r.Stage != "applied" {
		t.Fatal(r.Stage)
	}
	// Controller observations can age while the acknowledged data path keeps
	// running. Do not turn agent liveness into an automatic traffic outage.
	old := fmtTime(s.Now().Add(-3 * time.Minute))
	if _, err := s.db.Exec(`UPDATE agents SET last_seen_at=? WHERE server_id IN (?,?)`, old, r.Request.EntryServerID, r.Request.IXServerID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE network_snapshots SET received_at=? WHERE server_id IN (?,?)`, old, r.Request.EntryServerID, r.Request.IXServerID); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.AccessRouteLanding(ctx, r.ID); err != nil || !ok {
		t.Fatal("management liveness withdrew acknowledged path", err)
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "applied" {
		t.Fatal("management liveness closed data path", r.Stage)
	}
	now := fmtTime(s.Now())
	s.db.Exec(`UPDATE agents SET last_seen_at=?`, now)
	s.db.Exec(`UPDATE network_snapshots SET received_at=?`, now)
	_, n, ok, err := s.AccessRouteLanding(ctx, r.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	n.Params = json.RawMessage(`{"cipher":"2022-blake3-aes-128-gcm","password":"rotated-test-only"}`)
	n.ServerParams = json.RawMessage(`{"method":"2022-blake3-aes-128-gcm","password":"rotated-test-only"}`)
	if err = s.UpdateNodeCredentials(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err = s.AccessRouteLanding(ctx, r.ID); err != nil || ok {
		t.Fatal("stale receipt revived rotated source", err)
	}
	r = advanceRoute(t, s, r)
	ef, _ = s.GetPortForward(ctx, r.EntryForwardID)
	if ef.Enabled || r.Stage != "blocked" {
		t.Fatal("invalid source did not close entry", r.Stage)
	}
	applyRouteSource(t, s, r)
	r = advanceRoute(t, s, r)
	acknowledgeRouteServer(t, s, r.Request.EntryServerID)
	r = advanceRoute(t, s, r)
	if r.Stage != "applied" {
		t.Fatal("cannot recover", r.Stage)
	}
}
func TestAccessRouteStopWaitsForFinalForwardCleanup(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	r := createTestRoute(t, s, accessRouteFixture(t, s))
	applyRouteSource(t, s, r)
	r = advanceRoute(t, s, r)
	acknowledgeRouteServer(t, s, r.Request.IXServerID)
	r = advanceRoute(t, s, r)
	acknowledgeRouteServer(t, s, r.Request.EntryServerID)
	r = advanceRoute(t, s, r)
	if err := s.StopAccessRoute(ctx, r.ID, r.Revision, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "stopping_entry" {
		t.Fatal(r.Stage)
	}
	xf, _ := s.GetPortForward(ctx, r.IXForwardID)
	if xf.Retired {
		t.Fatal("IX cleaned before entry final receipt")
	}
	if _, _, ok, _ := s.AccessRouteLanding(ctx, r.ID); ok {
		t.Fatal("stopping route still exported")
	}
	// Simulate the final accounting transaction deleting the retired forward.
	if _, err := s.db.Exec(`DELETE FROM port_forwards WHERE id=? AND retired=1`, r.EntryForwardID); err != nil {
		t.Fatal(err)
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "stopping_ix" {
		t.Fatal(r.Stage)
	}
	xf, _ = s.GetPortForward(ctx, r.IXForwardID)
	if !xf.Retired {
		t.Fatal("IX not retired")
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "stopping_ix" {
		t.Fatal("released IX without final accounting")
	}
	s.db.Exec(`DELETE FROM port_forwards WHERE id=? AND retired=1`, r.IXForwardID)
	r = advanceRoute(t, s, r)
	if r.Stage != "retired" {
		t.Fatal(r.Stage)
	}
	if _, err := s.GetNode(ctx, r.Request.LandingNodeID); err != nil {
		t.Fatal("landing node deleted", err)
	}
	if err := s.DeleteServer(ctx, r.Request.EntryServerID); err != nil {
		t.Fatal("retired route retains active dependency", err)
	}
}
func TestPrivateAccessRouteRequiresExactLocalGrants(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := accessRouteFixture(t, s)
	in.IXHost = "10.42.0.2"
	v, err := s.PreviewAccessRoute(ctx, in)
	if err != nil || !v.Ready || !v.WaitingAuthorization {
		t.Fatal("missing deferred authorization", v.Ready, v.WaitingAuthorization, err, v.Entry.Checks)
	}
	r := createTestRoute(t, s, in)
	applyRouteSource(t, s, r)
	r = advanceRoute(t, s, r)
	acknowledgeRouteServer(t, s, r.Request.IXServerID)
	r = advanceRoute(t, s, r)
	ef, _ := s.GetPortForward(ctx, r.EntryForwardID)
	if ef.Enabled {
		t.Fatal("private target opened without local grant")
	}
	a, _ := s.GetAgentByServer(ctx, in.EntryServerID)
	var diag agentproto.Diagnostics
	json.Unmarshal(a.Diagnostics, &diag)
	diag.ForwardGrants = []networkconfig.ForwardGrant{{ForwardID: r.EntryForwardID, EgressProfileID: 0, Address: in.IXHost, Port: r.Request.IXPort}}
	writeNetworkDiagnostics(t, s, in.EntryServerID, diag)
	r = advanceRoute(t, s, r)
	ef, _ = s.GetPortForward(ctx, r.EntryForwardID)
	if !ef.Enabled {
		t.Fatal("matching local grant not used", r.Stage, r.Message)
	}
	acknowledgeRouteServer(t, s, r.Request.EntryServerID)
	r = advanceRoute(t, s, r)
	if _, _, ok, err := s.AccessRouteLanding(ctx, r.ID); err != nil || !ok {
		t.Fatal("authorized route not exported", err)
	}
	diag.ForwardGrants = nil
	writeNetworkDiagnostics(t, s, in.EntryServerID, diag)
	if _, _, ok, err := s.AccessRouteLanding(ctx, r.ID); err != nil || ok {
		t.Fatal("revoked local grant kept subscription live", err)
	}
	r = advanceRoute(t, s, r)
	ef, _ = s.GetPortForward(ctx, r.EntryForwardID)
	if ef.Enabled {
		t.Fatal("revoked grant kept entry intent enabled")
	}
	in.ID = strings.Repeat("e", 32)
	in.EntryPort++
	in.IXPort++
	in.Network = "both"
	v, err = s.PreviewAccessRoute(ctx, in)
	if err == nil && v.Ready {
		t.Fatal("unsupported private UDP admitted")
	}
}

func TestExternalLandingRefreshCannotSilentlyRedirectRoute(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "relay", true: "direct"}[direct], func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			in := accessRouteFixture(t, s)
			if direct {
				in.IXServerID = 0
				in.IXPort = 0
				in.IXHost = ""
			}
			ext := domain.ExternalSubscription{Name: "fixture feed", URL: "https://feed.example.test", Enabled: true}
			if err := s.CreateExternal(ctx, &ext); err != nil {
				t.Fatal(err)
			}
			fresh := []domain.Node{{Name: "external", Protocol: "vless", Server: "landing.example.test", Port: 443, Params: json.RawMessage(`{"uuid":"11111111-1111-1111-1111-111111111111","tls":true}`)}}
			if _, _, _, err := s.ReplaceExternalNodes(ctx, ext.ID, fresh); err != nil {
				t.Fatal(err)
			}
			nodes, err := s.ListNodes(ctx, NodeFilter{ExternalSubID: &ext.ID})
			if err != nil || len(nodes) != 1 {
				t.Fatal(err, nodes)
			}
			in.LandingNodeID = nodes[0].ID
			in.LandingHost = "203.0.113.30"
			in.LandingPort = 0
			r := createTestRoute(t, s, in)
			for i := 0; i < 8; i++ {
				r = advanceRoute(t, s, r)
				acknowledgeRouteServer(t, s, r.Request.EntryServerID)
				if !direct {
					acknowledgeRouteServer(t, s, r.Request.IXServerID)
				}
			}
			if r.Stage != "applied" {
				t.Fatal(r.Stage, r.Message)
			}
			if _, _, ok, err := s.AccessRouteLanding(ctx, r.ID); err != nil || !ok {
				t.Fatal("external node required a managed receipt", err)
			}
			if err := s.DeleteExternal(ctx, ext.ID); !errors.Is(err, ErrAccessRouteInUse) {
				t.Fatal("deleted a live feed", err)
			}
			changed := append([]domain.Node{}, fresh...)
			changed[0].Server = "changed.example.test"
			if _, _, _, err := s.ReplaceExternalNodes(ctx, ext.ID, changed); err != nil {
				t.Fatal(err)
			}
			if _, _, ok, err := s.AccessRouteLanding(ctx, r.ID); err != nil || ok {
				t.Fatal("upstream host change retained live export", err)
			}
			r = advanceRoute(t, s, r)
			if r.Stage != "blocked" {
				t.Fatal("did not close entry", r.Stage)
			}
			// Removing a referenced node must not roll back updates to the whole feed.
			other := domain.Node{Name: "other", Protocol: "ss", Server: "other.example.test", Port: 443, Params: json.RawMessage(`{"cipher":"aes-128-gcm","password":"fixture"}`)}
			if _, _, removed, err := s.ReplaceExternalNodes(ctx, ext.ID, []domain.Node{other}); err != nil || removed != 1 {
				t.Fatal("feed sync blocked by route FK", removed, err)
			}
			missing, err := s.GetNode(ctx, in.LandingNodeID)
			if err != nil || missing.Enabled || !missing.Revoked {
				t.Fatal("missing landing was not revoked", err)
			}
			available, err := s.ListNodes(ctx, NodeFilter{ExternalSubID: &ext.ID, OnlyEnabled: true})
			if err != nil || len(available) != 1 || available[0].Name != "other" {
				t.Fatal("unrelated feed changes lost", err)
			}
		})
	}
}

func TestRouteSourceTransportAndProtocolAreIndependent(t *testing.T) {
	for _, protocol := range []string{"vless", "vmess", "trojan", "anytls", "snell", "ss", "hysteria2", "tuic", "wireguard", "mieru"} {
		t.Run(protocol, func(t *testing.T) {
			n := domain.Node{Source: domain.NodeImported, Protocol: protocol, Server: "landing.example.test", Port: 443, Enabled: true, Params: json.RawMessage(`{"transport":"UDP"}`)}
			in := AccessRouteRequest{EntryServerID: 1, IXServerID: 2, LandingProtocol: protocol, LandingSourceHost: n.Server, LandingPort: n.Port, Network: "tcp"}
			udpOnly := protocol == "hysteria2" || protocol == "tuic" || protocol == "wireguard" || protocol == "mieru"
			if routeSourceValid(n, in) == udpOnly {
				t.Fatal("wrong wire transport admission", protocol)
			}
			in.Network = "both"
			if !routeSourceValid(n, in) {
				t.Fatal("UDP-capable route rejected", protocol)
			}
			in.LandingProtocol = "changed"
			if routeSourceValid(n, in) {
				t.Fatal("protocol changed under a live route")
			}
		})
	}
}

func directRouteFixture(t *testing.T, s *Store) AccessRouteRequest {
	in := accessRouteFixture(t, s)
	in.IXServerID, in.IXPort, in.IXHost = 0, 0, ""
	return in
}

func TestDirectAccessRouteLifecycleAndReceiptGating(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := directRouteFixture(t, s)
	r := createTestRoute(t, s, in)
	if r.IXForwardID != 0 || r.Request.IXServerID != 0 {
		t.Fatal("unexpected relay", r)
	}
	f, err := s.GetPortForward(ctx, r.EntryForwardID)
	if err != nil || f.Config.TargetHost != r.Request.LandingHost || f.Config.TargetPort != r.Request.LandingPort {
		t.Fatal("not forwarding directly to landing", f, err)
	}
	var count int
	s.db.QueryRow(`SELECT count(*) FROM port_forwards`).Scan(&count)
	if count != 1 {
		t.Fatal("extra listener", count)
	}
	views, err := s.PortForwardViews(ctx, in.EntryServerID)
	if err != nil || len(views) != 1 || views[0].RouteID != r.ID {
		t.Fatal("lost route ownership", err)
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "blocked" {
		t.Fatal("unapplied landing admitted", r.Stage)
	}
	applyRouteSource(t, s, r)
	r = advanceRoute(t, s, r)
	if r.Stage != "pending_entry" {
		t.Fatal("waiting for nonexistent relay", r.Stage)
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "pending_entry" {
		t.Fatal("entry receipt bypassed", r.Stage)
	}
	if _, _, ok, e := s.AccessRouteLanding(ctx, r.ID); e != nil || ok {
		t.Fatal("export before receipt", e)
	}
	acknowledgeRouteServer(t, s, in.EntryServerID)
	r = advanceRoute(t, s, r)
	if r.Stage != "applied" {
		t.Fatal(r.Stage, r.Message)
	}
	if _, _, ok, e := s.AccessRouteLanding(ctx, r.ID); e != nil || !ok {
		t.Fatal("direct route not exported", e)
	}
	if err = s.RetryAccessRoute(ctx, r.ID, r.Revision, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, e := s.AccessRouteLanding(ctx, r.ID); e != nil || ok {
		t.Fatal("retry kept stale export", e)
	}
	acknowledgeRouteServer(t, s, in.EntryServerID)
	r = advanceRoute(t, s, r)
	if err = s.StopAccessRoute(ctx, r.ID, r.Revision, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "stopping_entry" {
		t.Fatal("did not wait for accounting", r.Stage)
	}
	if _, _, ok, _ := s.AccessRouteLanding(ctx, r.ID); ok {
		t.Fatal("stopped route exported")
	}
	if _, err = s.db.Exec(`DELETE FROM port_forwards WHERE id=? AND retired=1`, r.EntryForwardID); err != nil {
		t.Fatal(err)
	}
	r = advanceRoute(t, s, r)
	if r.Stage != "retired" {
		t.Fatal("nonexistent relay blocked cleanup", r.Stage)
	}
	if _, err = s.GetNode(ctx, in.LandingNodeID); err != nil {
		t.Fatal("landing removed", err)
	}
}

func TestDirectAccessRouteRejectsRelayFieldsAndSelfLanding(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := directRouteFixture(t, s)
	for _, change := range []func(*AccessRouteRequest){
		func(r *AccessRouteRequest) { r.IXHost = "203.0.113.2" },
		func(r *AccessRouteRequest) { r.IXPort = 24001 },
		func(r *AccessRouteRequest) { r.IXEgressID = 1 },
		func(r *AccessRouteRequest) { r.IXEgressRevision = 1 },
		func(r *AccessRouteRequest) { n, _ := s.GetNode(ctx, r.LandingNodeID); r.EntryServerID = *n.ServerID },
	} {
		bad := in
		change(&bad)
		if _, err := s.PreviewAccessRoute(ctx, bad); !errors.Is(err, ErrAccessRoute) {
			t.Fatal("invalid direct route accepted", err)
		}
	}
	// Multiple direct routes must use NULL, not a shared sentinel relay ID.
	for i := 0; i < 2; i++ {
		candidate := in
		candidate.ID = strings.Repeat(string(rune('a'+i)), 32)
		candidate.EntryPort += i
		createTestRoute(t, s, candidate)
	}
}

func TestAccessRouteV40UpgradePreservesRelayAndOwnership(t *testing.T) {
	s, path := historicalStore(t, 40)
	ctx := context.Background()
	before := createTestRoute(t, s, accessRouteFixture(t, s))
	if _, err := s.db.Exec(`UPDATE access_routes SET stage='pending_ix' WHERE id=?`, before.ID); err != nil {
		t.Fatal(err)
	}
	before, _ = s.AccessRoute(ctx, before.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	after, err := upgraded.AccessRoute(ctx, before.ID)
	if err != nil || routeHash(before) != routeHash(after) {
		t.Fatal("migration changed existing route", err)
	}
	for _, fid := range []int64{after.EntryForwardID, after.IXForwardID} {
		if err := upgraded.RequireUnmanagedForward(ctx, upgraded.db, fid); !errors.Is(err, ErrRouteOwned) {
			t.Fatal("migration lost ownership", err)
		}
	}
	var integrity string
	if err := upgraded.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
	rows, err := upgraded.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke FK")
	}
}

func TestDirectPrivateRouteWaitsForExactGrant(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in := directRouteFixture(t, s)
	in.LandingHost = "10.42.0.2"
	v, err := s.PreviewAccessRoute(ctx, in)
	if err != nil || !v.Ready || !v.WaitingAuthorization {
		t.Fatal("private direct preview", err)
	}
	r := createTestRoute(t, s, in)
	applyRouteSource(t, s, r)
	r = advanceRoute(t, s, r)
	f, _ := s.GetPortForward(ctx, r.EntryForwardID)
	if f.Enabled {
		t.Fatal("private direct route opened without grant")
	}
	a, _ := s.GetAgentByServer(ctx, in.EntryServerID)
	var diag agentproto.Diagnostics
	json.Unmarshal(a.Diagnostics, &diag)
	diag.ForwardGrants = []networkconfig.ForwardGrant{{ForwardID: r.EntryForwardID, Address: in.LandingHost, Port: r.Request.LandingPort}}
	writeNetworkDiagnostics(t, s, in.EntryServerID, diag)
	r = advanceRoute(t, s, r)
	acknowledgeRouteServer(t, s, in.EntryServerID)
	r = advanceRoute(t, s, r)
	if r.Stage != "applied" {
		t.Fatal(r.Stage, r.Message)
	}
	diag.ForwardGrants = nil
	writeNetworkDiagnostics(t, s, in.EntryServerID, diag)
	if _, _, ok, e := s.AccessRouteLanding(ctx, r.ID); e != nil || ok {
		t.Fatal("grant revocation ignored", e)
	}
	r = advanceRoute(t, s, r)
	f, _ = s.GetPortForward(ctx, r.EntryForwardID)
	if f.Enabled {
		t.Fatal("grant revocation did not close entry")
	}
}
