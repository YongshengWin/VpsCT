package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

type bindingFixtureHost struct {
	mu                   sync.Mutex
	plan                 *networkguard.Plan
	snapshot             agentproto.NetworkSnapshot
	grants               map[int64]bool
	ids                  map[string]bool
	installs             int
	collectedBeforeFence bool
	watchErr             error
}

type bindingStopDriver struct {
	core.Driver
	stopped bool
}

func (d *bindingStopDriver) Stop(ctx context.Context) error {
	d.stopped = true
	return ctx.Err()
}

func TestBindingFenceFailureStopsOnlyRelatedManagedCores(t *testing.T) {
	sb, snell := &bindingStopDriver{}, &bindingStopDriver{}
	a := &Agent{Drivers: map[string]core.Driver{"singbox": sb, "snell": snell}}
	ds := &agentproto.DesiredState{Nodes: []agentproto.NodeSpec{{Core: "singbox", Network: &agentproto.NodeNetworkSpec{}}, {Core: "snell"}}}
	if err := a.stopBindingCores(ds); err != nil {
		t.Fatal(err)
	}
	if !sb.stopped || snell.stopped {
		t.Fatal("guard failure stopped an unrelated core")
	}
	a.bindings = &bindingRuntime{plan: &networkguard.Plan{Bindings: []networkguard.Binding{{Core: "snell"}}}}
	if err := a.stopBindingCores(&agentproto.DesiredState{}); err != nil {
		t.Fatal(err)
	}
	if !snell.stopped {
		t.Fatal("removed bindings escaped stop fallback")
	}
}

func TestNetworkIntentRejectsOlderDesiredAfterPartialFailureAndRestart(t *testing.T) {
	a := &Agent{StateDir: t.TempDir(), State: &State{ServerURL: "https://example.test", AgentToken: "fixture", ServerID: 1, AppliedRevision: 1}}
	ds := &agentproto.DesiredState{ServerID: 1, Revision: 2, Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 21001, Network: &agentproto.NodeNetworkSpec{Policy: networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}}}}}
	ds.NetworkBindingVersion = agentproto.NetworkBindingVersion
	ds.Hash = agentproto.ContentHash(ds)
	if err := a.rememberNetworkIntent(ds); err != nil {
		t.Fatal(err)
	}
	// No successful apply took place, but its security boundary is durable.
	st, err := LoadState(a.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.AppliedRevision != 1 {
		t.Fatal("intent falsely acknowledged an application")
	}
	revision, hash := st.desiredBoundary()
	if err = agentproto.ValidateDesired(ds, 1, revision, hash); err != nil {
		t.Fatal("same request cannot retry", err)
	}
	old := *ds
	old.Revision = 1
	if agentproto.ValidateDesired(&old, 1, revision, hash) == nil {
		t.Fatal("partial failure accepted an older request")
	}
	changed := *ds
	changed.IPv4Only = true
	changed.Hash = agentproto.ContentHash(&changed)
	if agentproto.ValidateDesired(&changed, 1, revision, hash) == nil {
		t.Fatal("same-revision content changed")
	}
	changed.Revision = 3
	if err = agentproto.ValidateDesired(&changed, 1, revision, hash); err != nil {
		t.Fatal("explicit newer rollback rejected", err)
	}
	// A newer revision from an older controller must not implicitly detach
	// all bindings. Explicit cleanup from a capable controller remains valid.
	a.State = st
	changed.Nodes = nil
	changed.NetworkBindingVersion = 0
	changed.Hash = agentproto.ContentHash(&changed)
	if a.rememberNetworkIntent(&changed) == nil {
		t.Fatal("new revision erased the persisted network contract")
	}
	changed.NetworkBindingVersion = agentproto.NetworkBindingVersion
	changed.Hash = agentproto.ContentHash(&changed)
	if err := a.rememberNetworkIntent(&changed); err != nil {
		t.Fatal("explicit cleanup rejected", err)
	}
	st, err = LoadState(a.StateDir)
	if err != nil || st.NetworkBindingVersion != agentproto.NetworkBindingVersion {
		t.Fatal("cleanup dropped sticky network requirement", err)
	}
}

func TestEgressIntentSurvivesRestartAndFinalDetach(t *testing.T) {
	a := &Agent{StateDir: t.TempDir(), State: &State{ServerURL: "https://example.test", AgentToken: "fixture", ServerID: 1}}
	ds := &agentproto.DesiredState{ServerID: 1, Revision: 2, NetworkBindingVersion: 1, NetworkEgressVersion: 1}
	ds.Hash = agentproto.ContentHash(ds)
	if err := a.rememberNetworkIntent(ds); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(a.StateDir)
	if err != nil || st.NetworkEgressVersion != 1 || st.AppliedRevision != 0 {
		t.Fatal("transit intent lost or falsely acknowledged", err)
	}
	a.State = st
	ds.Revision++
	ds.NetworkEgressVersion = 0
	ds.Hash = agentproto.ContentHash(ds)
	if a.rememberNetworkIntent(ds) == nil {
		t.Fatal("new direct-only cleanup erased transit contract")
	}
	ds.NetworkEgressVersion = 1
	ds.Hash = agentproto.ContentHash(ds)
	if err := a.rememberNetworkIntent(ds); err != nil {
		t.Fatal("capable cleanup rejected", err)
	}
	st, err = LoadState(a.StateDir)
	if err != nil || st.NetworkEgressVersion != 1 {
		t.Fatal("cleanup removed downgrade protection", err)
	}
}

func TestUnreadySOCKS5StaysFencedInsteadOfBecomingDirect(t *testing.T) {
	r, f, ds := bindingAgentFixture(t)
	// The peer intentionally retains the fixture's original direct policy.
	n := *ds.Nodes[0].Network
	cfg := networkconfig.SOCKS5{Server: "upstream.example.test", ServerPort: 1080, Authentication: "none", Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}, Outer: *n.Direct, ConnectTimeoutSeconds: 10}
	n.Direct, n.SOCKS5 = nil, &agentproto.SOCKS5Egress{Config: cfg}
	ds.Nodes[0].Network = &n
	tx, err := r.prepare(context.Background(), ds, nil)
	if err != nil || len(tx.failures) != 1 || len(tx.nodes) != 2 {
		t.Fatal("unready transport was not isolated from its peer", err)
	}
	for _, node := range tx.nodes {
		if node.NodeID == 1 {
			t.Fatal("unready SOCKS5 reached core application")
		}
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": true, "snell": true}); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, func() bool { return f.granted(2) })
	if f.granted(1) {
		t.Fatal("unready SOCKS5 acquired an activation lease")
	}
}

func TestLiteralSOCKS5ActivatesOnlyAfterSuccessfulCoreApply(t *testing.T) {
	r, f, ds := bindingAgentFixture(t)
	n := *ds.Nodes[0].Network
	cfg := networkconfig.SOCKS5{Server: "192.0.2.2", ServerPort: 1080, Authentication: "none", UDP: true, Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.53", Port: 53}, Outer: *n.Direct, ConnectTimeoutSeconds: 10}
	n.Direct, n.SOCKS5 = nil, &agentproto.SOCKS5Egress{Config: cfg}
	ds.Nodes[0].Network = &n
	tx, err := r.prepare(context.Background(), ds, nil)
	if err != nil || len(tx.failures) != 0 || len(tx.nodes) != 3 {
		t.Fatal("literal SOCKS5 did not reach candidate application", err)
	}
	if tx.nodes[0].RuntimeNetwork.SOCKS5 == nil || tx.nodes[0].RuntimeNetwork.SOCKS5.Address != cfg.Server {
		t.Fatal("candidate lost its local endpoint fence")
	}
	if f.granted(1) {
		t.Fatal("SOCKS5 activated before core application")
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": true, "snell": true}); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, func() bool { return f.granted(1) && f.granted(2) })
	// Switching to a presently unsupported hostname fences the formerly
	// active transport, even if the core driver reports a successful rollback.
	n.SOCKS5.Config.Server = "upstream.example.test"
	tx, err = r.prepare(context.Background(), ds, nil)
	if err != nil || len(tx.failures) != 1 {
		t.Fatal("unresolved replacement was accepted", err)
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": true, "snell": true}); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, func() bool { return f.granted(2) })
	if f.granted(1) {
		t.Fatal("failed replacement restored the previous upstream lease")
	}
}

func TestDomainSOCKS5RechecksLocalIdentityAfterDNS(t *testing.T) {
	for _, becameLocal := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "endpoint-became-local"}[becameLocal], func(t *testing.T) {
			r, f, ds := bindingAgentFixture(t)
			ds.Nodes = ds.Nodes[:1]
			n := ds.Nodes[0].Network
			cfg := networkconfig.SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "none", Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.53", Port: 53}, Outer: *n.Direct, ConnectTimeoutSeconds: 10}
			n.Direct, n.SOCKS5 = nil, &agentproto.SOCKS5Egress{Config: cfg}
			r.resolveSOCKS = func(_ context.Context, _ int64, policy networkconfig.Node, cfg networkconfig.SOCKS5, s *agentproto.NetworkSnapshot, ipv4 bool, now time.Time, _ []networkconfig.TransportGrant) (networkconfig.Resolved, error) {
				resolved, err := netinventory.ResolveBinding(policy, &cfg.Outer, s, ipv4, now)
				resolved.SOCKS5 = &networkconfig.ResolvedSOCKS5{Host: cfg.Server, Address: "192.0.2.2", Port: cfg.ServerPort}
				resolved.SOCKS5.SetDNSLifetime(now, 60)
				if becameLocal {
					f.mu.Lock()
					f.snapshot.Interfaces[0].Addresses = []string{"192.0.2.2/24"}
					f.mu.Unlock()
				}
				return resolved, err
			}
			tx, err := r.prepare(context.Background(), ds, nil)
			if err != nil {
				t.Fatal(err)
			}
			if becameLocal {
				if len(tx.nodes) != 0 || len(tx.failures) != 1 {
					t.Fatal("stale DNS candidate reached the core")
				}
			} else if len(tx.nodes) != 1 || len(tx.failures) != 0 {
				t.Fatal("stable domain candidate was rejected", tx.failures)
			}
			if f.granted(1) {
				t.Fatal("DNS resolution activated business before core application")
			}
		})
	}
}

func bindingAgentFixture(t *testing.T) (*bindingRuntime, *bindingFixtureHost, *agentproto.DesiredState) {
	t.Helper()
	id := strings.Repeat("1", 32)
	network := &agentproto.NodeNetworkSpec{Policy: networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}, Direct: &networkconfig.Direct{InterfaceID: id, Family: "dual", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}}
	ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.12.14"}}, Nodes: []agentproto.NodeSpec{
		{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 21001, Network: network, Params: map[string]any{"password": "fixture-old"}},
		{NodeID: 2, Core: "singbox", Protocol: "ss", ListenPort: 21002, Network: network},
		{NodeID: 3, Core: "snell", Protocol: "snell", ListenPort: 21003},
	}}
	f := &bindingFixtureHost{snapshot: agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "test", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: strings.Repeat("b", 32), Name: "wan1", Index: 2, Kind: "ether", Up: true, Carrier: true}}}}
	ctx, cancel := context.WithCancel(context.Background())
	r := &bindingRuntime{ctx: ctx}
	r.collect = func() *agentproto.NetworkSnapshot {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.plan == nil {
			f.collectedBeforeFence = true
		}
		f.snapshot.Sequence++
		s := f.snapshot
		s.Interfaces = append([]agentproto.NetworkInterface(nil), s.Interfaces...)
		s.SampledAt = time.Now()
		return &s
	}
	r.setIDs = func(ids map[string]bool) { f.mu.Lock(); defer f.mu.Unlock(); f.ids = ids }
	r.load = func(context.Context) (*networkguard.Plan, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.plan, nil
	}
	r.install = func(_ context.Context, p networkguard.Plan) error {
		if err := p.Validate(); err != nil {
			return err
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.plan, f.grants = &p, nil
		f.installs++
		return nil
	}
	r.refresh = func(_ context.Context, token string, s *agentproto.NetworkSnapshot) (map[int64]string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.plan == nil || f.plan.Token != token {
			return nil, errors.New("stale plan")
		}
		ready, bad := f.plan.Healthy(s, time.Now())
		f.grants = ready
		return bad, nil
	}
	r.renewDNS = func(_ context.Context, p networkguard.Plan) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.plan == nil || f.plan.Token != p.Token || !f.plan.SamePaths(p) {
			return errors.New("invalid DNS-only renewal")
		}
		if err := p.Validate(); err != nil {
			return err
		}
		f.plan = &p
		return nil
	}
	r.revoke = func(_ context.Context, token string, indices map[int]bool, lost bool) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.plan.Token != token {
			return errors.New("stale plan")
		}
		for id := range f.plan.Affected(indices, lost) {
			delete(f.grants, id)
		}
		return nil
	}
	r.watch = func(ctx context.Context) (<-chan netinventory.Change, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return make(chan netinventory.Change), f.watchErr
	}
	t.Cleanup(func() { cancel(); r.stop() })
	return r, f, ds
}

func waitBinding(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("binding state did not converge")
}

func (f *bindingFixtureHost) granted(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grants[id]
}

func TestBindingApplyFencesBeforePreflightAndActivatesOnlySuccessfulCore(t *testing.T) {
	r, f, ds := bindingAgentFixture(t)
	tx, err := r.prepare(context.Background(), ds, nil)
	if err != nil || len(tx.failures) != 0 || len(tx.nodes) != 3 {
		t.Fatal(tx, err)
	}
	f.mu.Lock()
	before := f.collectedBeforeFence
	selected := f.ids[strings.Repeat("1", 32)]
	f.mu.Unlock()
	if before || !selected {
		t.Fatal("local preflight preceded its fence or lost interface preference")
	}
	if f.granted(1) || f.granted(2) {
		t.Fatal("candidate activated before core application")
	}
	if err = tx.finish(context.Background(), map[string]bool{"snell": true}); err != nil {
		t.Fatal(err)
	}
	if f.granted(1) || f.granted(2) {
		t.Fatal("failed core activated")
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": true, "snell": true}); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, func() bool { return f.granted(1) && f.granted(2) })
	// A second, identical desired application is idempotent at staging.
	f.mu.Lock()
	installs := f.installs
	f.mu.Unlock()
	unchangedTx, err := r.prepare(context.Background(), ds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = unchangedTx.finish(context.Background(), map[string]bool{"singbox": true}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	unchanged := installs == f.installs
	f.mu.Unlock()
	if !unchanged {
		t.Fatal("unchanged config reinstalled its guard")
	}
	// Credentials changed: old processes may be rolled back by the driver,
	// but the changed node cannot regain a lease. Its unchanged peer can.
	ds.Nodes[0].Params["password"] = "fixture-new"
	tx, err = r.prepare(context.Background(), ds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": false}); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, func() bool { return !f.granted(1) && f.granted(2) })
	if !r.plan.Bindings[0].Pending {
		t.Fatal("rollback restored revoked credentials")
	}
}

func TestMissingBindingDoesNotPreventOtherNodesAndQuotaRemoval(t *testing.T) {
	r, f, ds := bindingAgentFixture(t)
	bad := *ds.Nodes[0].Network
	direct := *bad.Direct
	direct.InterfaceID = strings.Repeat("2", 32)
	bad.Direct = &direct
	ds.Nodes[0].Network = &bad
	ds.Nodes[2].Blocked = true
	tx, err := r.prepare(context.Background(), ds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.failures) != 1 || len(tx.nodes) != 2 || tx.nodes[0].NodeID != 2 || !tx.nodes[1].Blocked {
		t.Fatal("missing NIC prevented a peer or a quota block from reaching its core", tx)
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": true, "snell": true}); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, func() bool { return !f.granted(1) && f.granted(2) })
	// Explicitly blocked network nodes do not require a healthy NIC, and are
	// excluded from the shared config without a spurious application failure.
	ds.Nodes[0].Blocked = true
	tx, err = r.prepare(context.Background(), ds, nil)
	if err != nil || len(tx.failures) != 0 {
		t.Fatal("quota-blocked node required network preflight", err)
	}
	// Removing a binding stays fenced until that core actually applies.
	ds.Nodes[1].Network = nil
	tx, err = r.prepare(context.Background(), ds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.finish(context.Background(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if !r.plan.Bindings[1].Pending {
		t.Fatal("legacy transition unguarded before apply")
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": true}); err != nil {
		t.Fatal(err)
	}
	if len(r.plan.Bindings) != 1 || r.plan.Bindings[0].NodeID != 1 {
		t.Fatal("applied transition retained a stale guard")
	}
}

func TestBindingMonitorRecoversRenameAndReportsWatchFailure(t *testing.T) {
	r, f, ds := bindingAgentFixture(t)
	tx, err := r.prepare(context.Background(), ds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": true}); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, func() bool { return f.granted(1) })
	f.mu.Lock()
	f.snapshot.Interfaces[0].Name = "renamed1"
	f.mu.Unlock()
	waitBinding(t, r.needsApply)
	if f.granted(1) {
		t.Fatal("renamed device used stale core binding")
	}
	tx, err = r.prepare(context.Background(), ds, nil)
	if err != nil || tx.nodes[0].RuntimeNetwork.Direct.Interface.Name != "renamed1" {
		t.Fatal("rename not recompiled", err)
	}
	if err = tx.finish(context.Background(), map[string]bool{"singbox": true}); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, func() bool { return f.granted(1) && !r.needsApply() })
	r.stop()
	if f.granted(1) {
		t.Fatal("monitor stop left a live lease")
	}
	f.mu.Lock()
	f.watchErr = errors.New("fixture watch unavailable")
	f.mu.Unlock()
	if err = r.restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitBinding(t, r.needsApply)
	_, message := r.status()
	if message == "" || f.granted(1) {
		t.Fatal("watch failure was not reported and fenced")
	}
}
