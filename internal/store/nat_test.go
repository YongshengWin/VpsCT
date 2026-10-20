package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/networkconfig"
)

func natStoreFixture(t *testing.T) (*Store, domain.Server) {
	t.Helper()
	s := openTest(t)
	srv := domain.Server{Name: "NAT fixture", PublicHost: "192.0.2.10", Enabled: true}
	if err := s.CreateServer(context.Background(), &srv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET token_hash='fixture' WHERE server_id=?`, srv.ID); err != nil {
		t.Fatal(err)
	}
	natStoreReport(t, s, srv.ID, &agentproto.NATStatus{Version: 2, Source: "none", Status: "disabled"})
	return s, srv
}
func natStoreReport(t *testing.T, s *Store, id int64, n *agentproto.NATStatus) {
	t.Helper()
	d := agentproto.Diagnostics{SecurityVersion: 1, SecurityPolicy: true, NetworkConfigureAllowed: true, NAT: n}
	b, _ := json.Marshal(d)
	a, err := s.GetAgentByServer(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Heartbeat(context.Background(), a.ID, "test", "", "", nil, b); err != nil {
		t.Fatal(err)
	}
}
func natFixtureConfig() *natconfig.Config {
	return &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001, Count: 2, Network: "both"}}}
}
func natFixtureApplied(t *testing.T, s *Store, id int64, c *natconfig.Config, source string) {
	t.Helper()
	c, err := normalizeNAT(c)
	if err != nil {
		t.Fatal(err)
	}
	status := "applied"
	if len(c.Mappings) == 0 {
		status = "disabled"
	}
	natStoreReport(t, s, id, &agentproto.NATStatus{Version: 2, Source: source, Configured: len(c.Mappings) > 0, Status: status, Config: c})
}
func natFixtureNode(id int64, port int) domain.Node {
	return domain.Node{Name: "fixture node", Protocol: "trojan", Source: domain.NodeDeployed, ServerID: &id, Server: "192.0.2.10", Port: port, ListenPort: port, Core: domain.CoreSingBox, Enabled: true, Params: []byte(`{}`), ServerParams: []byte(`{}`)}
}

func TestNATCASDurablePublicationAndOrdinaryEdits(t *testing.T) {
	s, srv := natStoreFixture(t)
	ctx := context.Background()
	before, _ := s.NetworkGeneration(ctx, srv.ID)
	v, err := s.SaveServerNAT(ctx, srv.ID, 0, natFixtureConfig())
	if err != nil || v.Revision != 1 || !v.Pending {
		t.Fatal(v, err)
	}
	gen, _ := s.NetworkGeneration(ctx, srv.ID)
	if gen != before+1 {
		t.Fatal("NAT intent not in durable publication generation", gen, before)
	}
	v, err = s.SaveServerNAT(ctx, srv.ID, 1, natFixtureConfig())
	if err != nil || v.Revision != 1 {
		t.Fatal("idempotent save", v, err)
	}
	if _, err = s.SaveServerNAT(ctx, srv.ID, 0, natFixtureConfig()); !errors.Is(err, ErrNATRevision) {
		t.Fatal("stale CAS", err)
	}
	srv.Name = "edited"
	if err = s.UpdateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	c, r, err := s.ControllerNAT(ctx, srv.ID)
	if err != nil || r != 1 || !sameNAT(c, natFixtureConfig()) {
		t.Fatal("ordinary edit lost NAT", err)
	}
	srv.PublicHost = ""
	if err = s.UpdateServer(ctx, &srv); !errors.Is(err, ErrNATPublicHost) {
		t.Fatal("removed explicit ingress", err)
	}
	if _, err = s.NATPortConfig(ctx, srv.ID); !errors.Is(err, ErrNATNotReady) {
		t.Fatal("pending auto allocation", err)
	}
	natFixtureApplied(t, s, srv.ID, natFixtureConfig(), "controller")
	if _, err = s.NATPortConfig(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	v, err = s.SaveServerNAT(ctx, srv.ID, 1, nil)
	if err != nil || v.Revision != 2 || !v.Pending {
		t.Fatal("clear CAS", v, err)
	}
	c, r, err = s.ControllerNAT(ctx, srv.ID)
	if err != nil || c != nil || r != 2 {
		t.Fatal("clear must retain sticky revision", r, err)
	}
}

func TestNATAuthorityDisableReadinessAndError(t *testing.T) {
	s, srv := natStoreFixture(t)
	ctx := context.Background()
	if _, err := s.SaveServerNAT(ctx, srv.ID, 0, natFixtureConfig()); err != nil {
		t.Fatal(err)
	}
	empty := &natconfig.Config{Schema: 1}
	natFixtureApplied(t, s, srv.ID, empty, "local")
	v, _ := s.ServerNAT(ctx, srv.ID)
	if v.Editable || v.Pending || v.Report.Source != "local" {
		t.Fatal("local authority", v)
	}
	if c, err := s.NATPortConfig(ctx, srv.ID); err != nil || c != nil {
		t.Fatal("local empty must opt out", err)
	}
	if _, err := s.SaveServerNAT(ctx, srv.ID, 1, nil); !errors.Is(err, ErrNATEdit) {
		t.Fatal("local config editable", err)
	}
	future := s.Now().Add(3 * time.Minute)
	s.Now = func() time.Time { return future }
	if _, err := s.NATPortConfig(ctx, srv.ID); !errors.Is(err, ErrNATNotReady) {
		t.Fatal("stale empty report fell back", err)
	}
	natStoreReport(t, s, srv.ID, &agentproto.NATStatus{Version: 2, Source: "local", Status: "error", Error: "invalid local file"})
	if _, err := s.NATPortConfig(ctx, srv.ID); !errors.Is(err, ErrNATNotReady) {
		t.Fatal("bad local file fell back", err)
	}
	natStoreReport(t, s, srv.ID, &agentproto.NATStatus{Version: 2, Source: "none", Status: "disabled"})
	if _, err := s.SaveServerNAT(ctx, srv.ID, 1, empty); err != nil {
		t.Fatal(err)
	}
	natFixtureApplied(t, s, srv.ID, empty, "controller")
	if c, err := s.NATPortConfig(ctx, srv.ID); err != nil || c != nil {
		t.Fatal("confirmed controller disable blocked", err)
	}
	a, _ := s.GetAgentByServer(ctx, srv.ID)
	var d agentproto.Diagnostics
	json.Unmarshal(a.Diagnostics, &d)
	d.SecurityPaused = true
	b, _ := json.Marshal(d)
	s.Heartbeat(ctx, a.ID, "test", "", "", nil, b)
	if _, err := s.NATPortConfig(ctx, srv.ID); !errors.Is(err, ErrNATNotReady) {
		t.Fatal("paused disable bypassed policy", err)
	}
}

func TestNATPortsResourcesClearAndPrivateExceptions(t *testing.T) {
	s, srv := natStoreFixture(t)
	ctx := context.Background()
	if _, err := s.SaveServerNAT(ctx, srv.ID, 0, natFixtureConfig()); err != nil {
		t.Fatal(err)
	}
	natFixtureApplied(t, s, srv.ID, natFixtureConfig(), "controller")
	n := natFixtureNode(srv.ID, 30001)
	if err := s.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []*natconfig.Config{nil, {Schema: 1}} {
		if _, err := s.SaveServerNAT(ctx, srv.ID, 1, cfg); !errors.Is(err, ErrNATResources) {
			t.Fatal("removed required mapping", err)
		}
	}
	bad := natFixtureNode(srv.ID, 31000)
	if err := s.CreateNode(ctx, &bad); !errors.Is(err, ErrNATResources) {
		t.Fatal("unmapped manual port", err)
	}
	n.Enabled = false
	if err := s.UpdateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveServerNAT(ctx, srv.ID, 1, &natconfig.Config{Schema: 1}); err != nil {
		t.Fatal(err)
	}
	natFixtureApplied(t, s, srv.ID, &natconfig.Config{Schema: 1}, "controller")
	// Restore a mapped configuration that no longer covers the disabled node.
	c := natFixtureConfig()
	c.Mappings[0].PublicPort = 30003
	if _, err := s.SaveServerNAT(ctx, srv.ID, 2, c); err != nil {
		t.Fatal(err)
	}
	natFixtureApplied(t, s, srv.ID, c, "controller")
	n.Enabled = true
	if err := s.UpdateNode(ctx, &n); !errors.Is(err, ErrNATResources) {
		t.Fatal("reenabled uncovered port", err)
	}
	private := natFixtureNode(srv.ID, 31001)
	private.Server = "10.20.0.2"
	private.Network = &networkconfig.Node{ListenMode: "all", AdvertiseMode: "override", OnUnavailable: "block"}
	// Predicate tests don't bypass the independently enforced binding capability.
	if !natPrivateNode(private) {
		t.Fatal("private override not recognized")
	}
	if err := s.validateNATNode(ctx, s.db, private); err != nil {
		t.Fatal(err)
	}
	private.Server = "192.0.2.20"
	if err := s.validateNATNode(ctx, s.db, private); !errors.Is(err, ErrNATResources) {
		t.Fatal("public override bypass", err)
	}
	private.Server = "10.20.0.2"
	private.Network.AdvertiseMode = "inherit"
	if err := s.validateNATNode(ctx, s.db, private); !errors.Is(err, ErrNATResources) {
		t.Fatal("private binding incorrectly exempt", err)
	}
	private.ListenPort = 10001
	private.Network.AdvertiseMode = "override"
	if err := s.validateNATNode(ctx, s.db, private); !errors.Is(err, ErrNATResources) {
		t.Fatal("private local source conflict", err)
	}
}

func TestNATForwardPrimitivesAndAtomicShareBatch(t *testing.T) {
	s, srv := natStoreFixture(t)
	ctx := context.Background()
	if _, err := s.SaveServerNAT(ctx, srv.ID, 0, natFixtureConfig()); err != nil {
		t.Fatal(err)
	}
	natFixtureApplied(t, s, srv.ID, natFixtureConfig(), "controller")
	if err := s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: "1.14.1"}); err != nil {
		t.Fatal(err)
	}
	f := forwardFixture(srv.ID, 31000)
	save := func() error {
		return s.Tx(ctx, func(tx *sql.Tx) error { var err error; f, err = s.createPortForward(ctx, tx, f); return err })
	}
	if err := save(); !errors.Is(err, ErrNATResources) {
		t.Fatal("direct forward create bypass", err)
	}
	f.Config.ListenPort = 30001
	if err := save(); err != nil {
		t.Fatal(err)
	}
	cfg := f.Config
	cfg.ListenPort = 31000
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		_, err := s.updatePortForward(ctx, tx, f.ID, 1, "updated", true, cfg)
		return err
	}); !errors.Is(err, ErrNATResources) {
		t.Fatal("direct forward update bypass", err)
	}
	// A provider change after a share preflight cannot leave its first node.
	creates := []domain.Node{natFixtureNode(srv.ID, 30002), natFixtureNode(srv.ID, 31000)}
	if err := s.ApplyShareNodeChanges(ctx, creates, nil); !errors.Is(err, ErrNATResources) {
		t.Fatal("batch unexpectedly succeeded", err)
	}
	var count int
	s.db.QueryRow(`SELECT count(*) FROM nodes WHERE server_id=?`, srv.ID).Scan(&count)
	if count != 0 {
		t.Fatal("partial batch node persisted", count)
	}
	cfg.ListenMode = "address"
	cfg.ListenAddress = "10.20.0.2"
	cfg.ListenInterfaceID = strings.Repeat("a", 32)
	cfg.SourceCIDRs = []string{"10.20.0.0/24"}
	if !natPrivateForward(cfg) || s.validateNATForward(ctx, s.db, domain.PortForward{ServerID: srv.ID, Enabled: true, Config: cfg}) != nil {
		t.Fatal("private forward not exempt")
	}
	cfg.SourceMode = "all"
	cfg.SourceCIDRs = nil
	if err := s.validateNATForward(ctx, s.db, domain.PortForward{ServerID: srv.ID, Enabled: true, Config: cfg}); !errors.Is(err, ErrNATResources) {
		t.Fatal("NAT private destination incorrectly exempt", err)
	}
}

func TestNATExplicitPublicAddress(t *testing.T) {
	for _, host := range []string{"", "10.0.0.1", "100.64.0.1", "0.1.2.3", "169.254.1.2", "240.1.2.3", "::1", "2001:db8::1"} {
		if validateNATPublicHost(host) == nil {
			t.Fatalf("accepted nonpublic %q", host)
		}
	}
	for _, host := range []string{"192.0.2.10", "nat.example.test"} {
		if err := validateNATPublicHost(host); err != nil {
			t.Fatal(err)
		}
	}
	s, srv := natStoreFixture(t)
	srv.PublicHost = ""
	if err := s.UpdateServer(context.Background(), &srv); err != nil {
		t.Fatal(err)
	}
	natFixtureApplied(t, s, srv.ID, natFixtureConfig(), "local")
	n := natFixtureNode(srv.ID, 30001)
	if err := s.CreateNode(context.Background(), &n); !errors.Is(err, ErrNATPublicHost) {
		t.Fatal("local report inferred public address", err)
	}
}

func TestNATMieruTransportUpdateAndCredentialsCannotBypassPool(t *testing.T) {
	s, srv := natStoreFixture(t)
	ctx := context.Background()
	c := natFixtureConfig()
	c.Mappings[0].Network = "tcp"
	if _, err := s.SaveServerNAT(ctx, srv.ID, 0, c); err != nil {
		t.Fatal(err)
	}
	natFixtureApplied(t, s, srv.ID, c, "controller")
	n := natFixtureNode(srv.ID, 30001)
	n.Protocol = "mieru"
	n.ServerParams = []byte(`{"transport":"TCP"}`)
	if err := s.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	n.ServerParams = []byte(`{"transport":"UDP"}`)
	if err := s.UpdateNode(ctx, &n); !errors.Is(err, ErrNATResources) {
		t.Fatal("same-port transport update bypass", err)
	}
	if err := s.UpdateNodeCredentials(ctx, &n); !errors.Is(err, ErrNATResources) {
		t.Fatal("credentials path changed transport", err)
	}
	got, _ := s.GetNode(ctx, n.ID)
	if string(got.ServerParams) != `{"transport":"TCP"}` {
		t.Fatal("failed transport update persisted")
	}
}

func TestNATConcurrentCASOnlyReturnsRevisionConflict(t *testing.T) {
	s, srv := natStoreFixture(t)
	ctx := context.Background()
	now := s.Now()
	var calls atomic.Int32
	barrier := make(chan struct{})
	s.Now = func() time.Time {
		n := calls.Add(1)
		if n == 4 {
			close(barrier)
		}
		if n <= 4 {
			<-barrier
		}
		return now
	}
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func(i int) {
			c := natFixtureConfig()
			c.Mappings[0].PublicPort += i * 10
			_, err := s.SaveServerNAT(ctx, srv.ID, 0, c)
			results <- err
		}(i)
	}
	successes := 0
	for i := 0; i < 4; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrNATRevision) {
			t.Fatal("CAS surfaced raw database error", err)
		}
	}
	if successes != 1 {
		t.Fatal("CAS successes", successes)
	}
	c, r, err := s.ControllerNAT(ctx, srv.ID)
	if err != nil || c == nil || r != 1 {
		t.Fatal("CAS lost winner", r, err)
	}
}

func TestNATReviewedForwardCreateAndUpdateAdmission(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	nr, diag, _ := readyNetworkFixture(t, s)
	node, _ := s.GetNode(ctx, nr.NodeID)
	id := *node.ServerID
	c := natFixtureConfig()
	c.Mappings[0].PublicPort = 25101
	c.Mappings[0].Count = 1
	c.Mappings[0].Network = "tcp"
	diag.NetworkForwardVersion = 1
	diag.NAT = &agentproto.NATStatus{Version: 2, Source: "local", Status: "applied", Configured: true, Config: c}
	writeNetworkDiagnostics(t, s, id, diag)
	f := forwardFixture(id, 25102)
	f.Config.TargetHost = "203.0.113.10"
	in := PortForwardRequest{ID: strings.Repeat("9", 32), Action: "create", ServerID: id, Name: f.Name, Enabled: true, Config: &f.Config}
	v, err := s.PreviewPortForward(ctx, in)
	if err != nil || v.Ready {
		t.Fatal("review admitted unmapped create", v.Ready, err)
	}
	in.ExpectedImpact = v.Impact.Token
	if _, err = s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkNotReady) {
		t.Fatal("reviewed create bypass", err)
	}
	f.Config.ListenPort = 25101
	in.Config = &f.Config
	v, err = s.PreviewPortForward(ctx, in)
	if err != nil || !v.Ready {
		t.Fatal("mapped create not ready", v.Checks, err)
	}
	in.ExpectedImpact = v.Impact.Token
	op, err := s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	in.ID = strings.Repeat("8", 32)
	in.Action = "update"
	in.ServerID = 0
	in.ForwardID = op.ResourceID
	in.ExpectedRevision = 1
	in.ExpectedImpact = ""
	f.Config.ListenPort = 25102
	v, err = s.PreviewPortForward(ctx, in)
	if err != nil || v.Ready {
		t.Fatal("review admitted unmapped update", v.Ready, err)
	}
	in.ExpectedImpact = v.Impact.Token
	if _, err = s.RequestReviewedPortForward(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkNotReady) {
		t.Fatal("reviewed update bypass", err)
	}
	rows, _ := s.ListPortForwards(ctx, id)
	if len(rows) != 1 || rows[0].Config.ListenPort != 25101 {
		t.Fatal("failed review mutated forward", rows)
	}
}
