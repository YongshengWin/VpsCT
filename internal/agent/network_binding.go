package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/proxyguard"
)

// bindingRuntime's monitor never acquires Agent.stateMu or performs HTTP. Its
// lifetime is the agent Run context, not a single apply or heartbeat request.
// Plan publication is serialized by the caller; health has its own mutex.
type bindingRuntime struct {
	ctx              context.Context
	collect          func() *agentproto.NetworkSnapshot
	setIDs           func(map[string]bool)
	load             func(context.Context) (*networkguard.Plan, error)
	install          func(context.Context, networkguard.Plan) error
	renewDNS         func(context.Context, networkguard.Plan) error
	refresh          func(context.Context, string, *agentproto.NetworkSnapshot) (map[int64]string, error)
	refreshResources func(context.Context, string, *agentproto.NetworkSnapshot) (map[agentproto.ResourceIdentity]string, error)
	forwardApplied   func(context.Context, string) error
	revoke           func(context.Context, string, map[int]bool, bool) error
	watch            func(context.Context) (<-chan netinventory.Change, error)
	resolveSOCKS     func(context.Context, int64, networkconfig.Node, networkconfig.SOCKS5, *agentproto.NetworkSnapshot, bool, time.Time, []networkconfig.TransportGrant) (networkconfig.Resolved, error)

	plan          *networkguard.Plan
	cancel        context.CancelFunc
	done          chan struct{}
	mu            sync.Mutex
	health        map[int64]string
	forwardHealth map[int64]string
	err           string
	// Serialized with plan publication. Resume after the last attempted DNS
	// lookup so failing early nodes cannot consume every subsequent batch.
	nextDNSNode    int64
	nextDNSForward int64
}

func newBindingRuntime(ctx context.Context, c *netinventory.Collector) *bindingRuntime {
	return &bindingRuntime{ctx: ctx, collect: c.Collect, setIDs: c.SetBindingIDs,
		load: proxyguard.LoadNetwork, install: proxyguard.InstallNetwork, renewDNS: proxyguard.RenewNetworkDNS,
		refresh: proxyguard.RefreshNetwork, revoke: proxyguard.RevokeNetwork,
		refreshResources: proxyguard.RefreshResources, forwardApplied: proxyguard.ForwardCoreApplied,
		resolveSOCKS: netinventory.ResolveSOCKS5WithGrants}
}

func (r *bindingRuntime) stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done // Collect is bounded; Run revokes before releasing this waiter.
		r.cancel, r.done = nil, nil
	}
}

func (r *bindingRuntime) status() (map[int64]string, string) {
	if r == nil {
		return nil, ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := make(map[int64]string, len(r.health))
	for id, message := range r.health {
		if len(message) > 512 {
			message = "节点网络不可用，请查看 agent 日志"
		}
		copy[id] = message
	}
	err := r.err
	if len(err) > 512 {
		err = "本机网络保护不可用，请查看 agent 日志"
	}
	return copy, err
}

func (r *bindingRuntime) needsApply() bool {
	health, err := r.status()
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(health) > 0 || len(r.forwardHealth) > 0 || err != ""
}

func (r *bindingRuntime) start(plan networkguard.Plan) {
	r.plan = &plan
	r.mu.Lock()
	r.health, r.forwardHealth, r.err = nil, nil, ""
	r.mu.Unlock()
	if len(plan.Bindings) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	r.cancel, r.done = cancel, done
	go func() {
		defer close(done)
		monitor := networkguard.Monitor{Collect: r.collect, Watch: r.watch,
			Renew: func(ctx context.Context, snapshot *agentproto.NetworkSnapshot) error {
				health, forwardHealth, err := r.refreshHealth(ctx, plan.Token, snapshot)
				// Deliberately blocked or failed candidates have no renewable
				// lease; their apply error is reported by the normal path.
				for _, b := range plan.Bindings {
					if b.Pending {
						if b.ForwardID != 0 {
							delete(forwardHealth, b.ForwardID)
						} else {
							delete(health, b.NodeID)
						}
					}
				}
				r.mu.Lock()
				r.health = health
				r.forwardHealth = forwardHealth
				r.mu.Unlock()
				return err
			},
			Revoke: func(ctx context.Context, indices map[int]bool, lost bool) error {
				return r.revoke(ctx, plan.Token, indices, lost)
			}}
		if err := monitor.Run(ctx); err != nil && ctx.Err() == nil {
			r.mu.Lock()
			r.err = "本机网络保护停止：" + err.Error()
			r.mu.Unlock()
		}
	}()
}

// restore only reads trusted current-boot local intent. Pending candidates
// never become active just because the agent restarts or loses its controller.
func (r *bindingRuntime) restore(ctx context.Context) error {
	plan, err := r.load(ctx)
	if err != nil || plan == nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	r.setIDs(bindingIDs(nil, plan))
	r.start(*plan)
	return nil
}

func (r *bindingRuntime) replace(ctx context.Context, bindings []networkguard.Binding) error {
	sort.Slice(bindings, func(i, j int) bool {
		a, b := bindings[i].Resource(), bindings[j].Resource()
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	// Stable applications don't flush healthy leases or restart the monitor.
	if r.plan != nil && reflect.DeepEqual(r.plan.Bindings, bindings) && !r.needsApply() {
		return nil
	}
	if r.plan != nil && r.renewDNS != nil {
		next := networkguard.Plan{Token: r.plan.Token, Bindings: bindings}
		if r.plan.SamePaths(next) && !reflect.DeepEqual(r.plan.Bindings, bindings) {
			if err := r.renewDNS(ctx, next); err != nil {
				return err
			}
			r.plan = &next
			return nil // keep the running monitor and existing kernel leases
		}
	}
	plan, err := networkguard.New(bindings)
	if err != nil {
		return err
	}
	r.stop()
	if err := r.install(ctx, plan); err != nil {
		r.mu.Lock()
		r.err = "本机网络保护安装失败：" + err.Error()
		r.mu.Unlock()
		return err
	}
	r.start(plan)
	return nil
}

func bindingFingerprint(n agentproto.NodeSpec, ipv4Only bool) string {
	// Remote config, credentials and local access permission all participate.
	// Only the digest is retained in the root-owned guard plan.
	b, _ := json.Marshal(struct {
		Node              agentproto.NodeSpec
		Private, IPv4Only bool
		TransportGrants   []networkconfig.TransportGrant
	}{n, n.AllowPrivate, ipv4Only, n.TransportGrants})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func pendingBinding(b networkguard.Binding) networkguard.Binding {
	b.Pending, b.Applied = true, networkconfig.Resolved{}
	return b
}

func bindingIDs(nodes []agentproto.NodeSpec, plan *networkguard.Plan) map[string]bool {
	ids := map[string]bool{}
	add := func(n networkconfig.Node, d *networkconfig.Direct) {
		if n.ListenInterfaceID != "" {
			ids[n.ListenInterfaceID] = true
		}
		if d != nil {
			if d.InterfaceID != "" {
				ids[d.InterfaceID] = true
			}
			for _, source := range []*networkconfig.Address{d.SourceIPv4, d.SourceIPv6} {
				if source != nil {
					ids[source.InterfaceID] = true
				}
			}
		}
	}
	for _, n := range nodes {
		if n.Network != nil {
			add(n.Network.Policy, n.Network.OuterBinding())
		}
	}
	if plan != nil {
		for _, b := range plan.Bindings {
			var d *networkconfig.Direct
			if b.Applied.Direct != nil {
				d = &b.Applied.Direct.Config
			}
			add(b.Wanted, d)
		}
	}
	return ids
}

type bindingApply struct {
	runtime         *bindingRuntime
	desired         []agentproto.NodeSpec
	nodes           []agentproto.NodeSpec
	staged          map[agentproto.ResourceIdentity]networkguard.Binding
	candidates      map[agentproto.ResourceIdentity]networkguard.Binding
	wanted          map[agentproto.ResourceIdentity]bool
	failures        []error
	desiredForwards []agentproto.ForwardSpec
	forwards        []agentproto.ForwardSpec
}

// prepare fences changed/removed bindings BEFORE any local network preflight.
// Unchanged bindings may continue renewing. Failed candidates are excluded from
// the core config so another node's missing NIC cannot prevent revocations.
func (r *bindingRuntime) prepare(ctx context.Context, ds *agentproto.DesiredState, groups map[int64]string) (*bindingApply, error) {
	tx := &bindingApply{runtime: r, desired: ds.Nodes, desiredForwards: ds.Forwards, staged: map[agentproto.ResourceIdentity]networkguard.Binding{}, candidates: map[agentproto.ResourceIdentity]networkguard.Binding{}, wanted: map[agentproto.ResourceIdentity]bool{}}
	old := map[agentproto.ResourceIdentity]networkguard.Binding{}
	if r.plan == nil {
		plan, err := r.load(ctx)
		if err != nil {
			return nil, err
		}
		r.plan = plan
	}
	if r.plan != nil {
		for _, b := range r.plan.Bindings {
			old[b.Resource()] = b
			tx.staged[b.Resource()] = pendingBinding(b)
		}
	}
	for _, n := range ds.Nodes {
		if n.Network == nil {
			continue
		}
		tx.wanted[nodeResource(n.NodeID)] = true
		b := networkguard.Binding{Pending: true, NodeID: n.NodeID, ListenPort: n.ListenPort, Core: n.Core, Wanted: n.Network.Policy, IPv4Only: ds.IPv4Only, Fingerprint: bindingFingerprint(n, ds.IPv4Only)}
		if core.IsStandalone(n.Core) {
			b.Group = groups[n.NodeID]
		}
		if prior, ok := old[nodeResource(n.NodeID)]; ok && !prior.Pending && !n.Blocked && prior.Fingerprint == b.Fingerprint {
			b = prior
		}
		tx.staged[nodeResource(n.NodeID)] = b
	}
	tx.stageForwards(ds, old)
	r.setIDs(managedBindingIDs(ds.Nodes, ds.Forwards, r.plan))
	if len(tx.staged) == 0 {
		tx.nodes = append([]agentproto.NodeSpec(nil), ds.Nodes...)
		return tx, nil
	}
	if err := r.replace(ctx, bindingValues(tx.staged)); err != nil {
		return nil, err
	}
	snapshot := r.collect()
	dnsContext, cancelDNS := context.WithTimeout(ctx, 10*time.Second)
	defer cancelDNS()
	start := 0
	for i, n := range ds.Nodes {
		if n.NodeID == r.nextDNSNode {
			start = i
			break
		}
	}
	prepared := make(map[int64]agentproto.NodeSpec, len(ds.Nodes))
	for step := range ds.Nodes {
		i := (start + step) % len(ds.Nodes)
		n := ds.Nodes[i]
		if n.Network == nil {
			prepared[n.NodeID] = n
			continue
		}
		if n.Blocked {
			continue
		}
		key := agentproto.CoreVersionKey(n.Core)
		var err error
		var resolved networkconfig.Resolved
		if snapshot == nil || time.Since(snapshot.SampledAt) > time.Second {
			snapshot = r.collect()
		}
		if !agentproto.NetworkBindingSupported(n.Core, ds.Versions[key].Version) {
			err = errors.New("所选内核版本尚未通过网络绑定验证")
		} else if n.Network.HasTransport() {
			transport, _ := n.Network.TransportConfig()
			if !agentproto.SOCKS5BindingSupported(n, ds.Versions[key].Version) {
				err = errors.New("该入口协议与 SOCKS5 出口组合尚未通过验证")
			} else {
				prior := tx.staged[nodeResource(n.NodeID)]
				pin := prior.Applied.SOCKS5
				if !prior.Pending && pin != nil && pin.Host != "" && !pin.DNSRefreshDue(time.Now()) {
					resolved, err = netinventory.ResolveBinding(n.Network.Policy, n.Network.OuterBinding(), snapshot, ds.IPv4Only, time.Now())
					resolved.SOCKS5 = pin
				} else if r.resolveSOCKS == nil {
					resolved, err = netinventory.ResolveSOCKS5(n.Network.Policy, transport, snapshot, ds.IPv4Only, time.Now())
				} else if _, literal := networkconfig.HostAddress(transport.Server); literal == nil {
					// Literal endpoints need no DNS budget, even after another
					// node's queries exhaust it.
					resolved, err = r.resolveSOCKS(ctx, n.NodeID, n.Network.Policy, transport, snapshot, ds.IPv4Only, time.Now(), n.TransportGrants)
				} else if dnsContext.Err() != nil {
					err = errors.New("本轮启动 DNS 查询预算已用尽，将在后续轮次继续")
				} else {
					r.nextDNSNode = ds.Nodes[(i+1)%len(ds.Nodes)].NodeID
					resolved, err = r.resolveSOCKS(dnsContext, n.NodeID, n.Network.Policy, transport, snapshot, ds.IPv4Only, time.Now(), n.TransportGrants)
				}
				if err == nil && resolved.SOCKS5 != nil && resolved.SOCKS5.Host != "" {
					// DNS may take seconds. Recheck binding identity and local
					// addresses using a fresh sample before handing it to core.
					snapshot = r.collect()
					pin := resolved.SOCKS5
					resolved, err = netinventory.ResolveBinding(n.Network.Policy, n.Network.OuterBinding(), snapshot, ds.IPv4Only, time.Now())
					if err == nil {
						err = netinventory.ValidateSOCKS5Endpoint(*pin, snapshot)
						resolved.SOCKS5 = pin
					}
				}
			}
		} else {
			resolved, err = netinventory.ResolveBinding(n.Network.Policy, n.Network.Direct, snapshot, ds.IPv4Only, time.Now())
		}
		if err != nil {
			if n.Network.HasTransport() {
				tx.staged[nodeResource(n.NodeID)] = pendingBinding(tx.staged[nodeResource(n.NodeID)])
			}
			tx.failures = append(tx.failures, fmt.Errorf("node %d network: %w", n.NodeID, err))
			continue
		}
		n.RuntimeNetwork = &resolved
		prepared[n.NodeID] = n
		b := tx.staged[nodeResource(n.NodeID)]
		// A newer sample alone does not require a new firewall application.
		// Keep the old minimum sequence when the running binding is identical.
		comparable := resolved
		comparable.Sequence = b.Applied.Sequence
		if !b.Pending && sameResolvedPath(comparable, b.Applied) {
			resolved.Sequence = b.Applied.Sequence
		} else if !b.Pending && n.Network.HasTransport() {
			tx.staged[nodeResource(n.NodeID)] = pendingBinding(b)
		}
		b.Pending, b.Applied = false, resolved
		tx.candidates[nodeResource(n.NodeID)] = b
	}
	// Rotation is a scheduling choice, never a core configuration change.
	for _, n := range ds.Nodes {
		if ready, ok := prepared[n.NodeID]; ok {
			tx.nodes = append(tx.nodes, ready)
		}
	}
	tx.prepareForwards(dnsContext, ds)
	// A DNS answer change is a path change even when remote desired is equal.
	// Fence the old endpoint before a core update or rollback can use it again.
	if err := r.replace(ctx, bindingValues(tx.staged)); err != nil {
		return nil, err
	}
	return tx, nil
}

func sameResolvedPath(a, b networkconfig.Resolved) bool {
	if a.SOCKS5 != nil {
		pin := a.SOCKS5.WithoutDNSLifetime()
		a.SOCKS5 = &pin
	}
	if b.SOCKS5 != nil {
		pin := b.SOCKS5.WithoutDNSLifetime()
		b.SOCKS5 = &pin
	}
	if a.ForwardTarget != nil {
		pin := a.ForwardTarget.WithoutDNSLifetime()
		a.ForwardTarget = &pin
	}
	if b.ForwardTarget != nil {
		pin := b.ForwardTarget.WithoutDNSLifetime()
		b.ForwardTarget = &pin
	}
	return reflect.DeepEqual(a, b)
}

// finish activates only successfully applied cores. A driver's rollback may
// restore a process, but never restores an old lease for changed credentials.
func (tx *bindingApply) finish(ctx context.Context, applied map[string]bool) error {
	if len(tx.staged) == 0 {
		return nil
	}
	final := map[agentproto.ResourceIdentity]networkguard.Binding{}
	for id, b := range tx.staged {
		if applied[b.Core] {
			if !tx.wanted[id] {
				continue // deleted node or successfully reverted to legacy mode
			}
			if candidate, ok := tx.candidates[id]; ok {
				b = candidate
			} else {
				b = pendingBinding(b)
			}
		}
		final[id] = b
	}
	if err := tx.runtime.replace(ctx, bindingValues(final)); err != nil {
		return err
	}
	if applied["singbox"] && tx.runtime.forwardApplied != nil && tx.runtime.plan != nil && (len(tx.desiredForwards) > 0 || tx.runtime.plan.HasForwards()) {
		if err := tx.runtime.forwardApplied(ctx, tx.runtime.plan.Token); err != nil {
			return err
		}
	}
	tx.runtime.setIDs(managedBindingIDs(tx.desired, tx.desiredForwards, tx.runtime.plan))
	return nil
}

func bindingValues(m map[agentproto.ResourceIdentity]networkguard.Binding) []networkguard.Binding {
	out := make([]networkguard.Binding, 0, len(m))
	for _, b := range m {
		out = append(out, b)
	}
	return out
}

// Persist the accepted revision before any restrictive change. A partially
// failed apply must not accept an older desired state which reintroduces a
// revoked credential. Rollback requires a new, explicit controller revision.
func (a *Agent) rememberNetworkIntent(ds *agentproto.DesiredState) error {
	required := a.State.NetworkBindingVersion
	egressRequired := a.State.NetworkEgressVersion
	forwardRequired := a.State.NetworkForwardVersion
	sshRequired := a.State.NetworkSSHVersion
	wgRequired := a.State.NetworkWireGuardVersion
	mitaRequired := a.State.MitaVersion
	if len(ds.Forwards) > 0 {
		forwardRequired = agentproto.NetworkForwardVersion
		required = agentproto.NetworkBindingVersion
	}
	if a.State.NetworkDesiredRevision > 0 || (a.bindings != nil && a.bindings.plan != nil && len(a.bindings.plan.Bindings) > 0) {
		required = max(required, agentproto.NetworkBindingVersion)
	}
	for _, n := range ds.Nodes {
		if n.Core == "mieru" {
			mitaRequired = networkconfig.MitaVersion
		}
		if n.Network != nil {
			required = max(required, agentproto.NetworkBindingVersion)
			if n.Network.WireGuard != nil {
				wgRequired = agentproto.NetworkWireGuardVersion
			}
			if n.Network.SSH != nil {
				sshRequired = agentproto.NetworkSSHVersion
			}
			if n.Network.HasTransport() {
				egressRequired = max(egressRequired, agentproto.NetworkEgressVersion)
			}
		}
	}
	if ds.NetworkBindingVersion < required {
		return errors.New("控制端缺少已启用的网络绑定版本，拒绝回退到旧配置语义")
	}
	if ds.NetworkEgressVersion < egressRequired {
		return errors.New("控制端缺少已启用的中转出口版本，拒绝回退到直连配置语义")
	}
	if ds.NetworkWireGuardVersion < wgRequired {
		return errors.New("控制端缺少已启用的 WireGuard 版本，拒绝回退")
	}
	if ds.NetworkSSHVersion < sshRequired {
		return errors.New("控制端缺少已启用的 SSH 中转版本，拒绝回退")
	}
	if ds.NetworkForwardVersion < forwardRequired {
		return errors.New("控制端缺少已启用的固定转发版本，拒绝回退")
	}
	if ds.MitaVersion < mitaRequired {
		return errors.New("控制端缺少已启用的 mita 能力，拒绝回退")
	}
	a.State.MitaVersion = ds.MitaVersion
	for _, n := range ds.Nodes {
		if n.Network != nil && n.Core != "singbox" {
			a.State.ListenBindingVersion = 1
		}
	}
	for _, f := range ds.Forwards {
		if f.HasTransport() {
			a.State.ForwardTransportVersion = 1
		}
		if ip, err := networkconfig.HostAddress(f.Config.TargetHost); err != nil {
			a.State.ForwardDNSVersion = 1
		} else if networkconfig.NeedsTransportGrant(ip) {
			a.State.ForwardPrivateVersion = 1
		}
		if len(f.ForwardGrants) > 0 {
			a.State.ForwardPrivateVersion = 1
		}
	}
	if ds.NetworkBindingVersion == 0 {
		if mitaRequired > 0 {
			return a.State.Save(a.StateDir)
		}
		return nil
	}
	a.State.NetworkDesiredRevision, a.State.NetworkDesiredHash = ds.Revision, ds.Hash
	a.State.NetworkBindingVersion = ds.NetworkBindingVersion
	a.State.NetworkEgressVersion = ds.NetworkEgressVersion
	a.State.NetworkSSHVersion = ds.NetworkSSHVersion
	a.State.NetworkWireGuardVersion = ds.NetworkWireGuardVersion
	a.State.NetworkForwardVersion = ds.NetworkForwardVersion
	return a.State.Save(a.StateDir)
}

// If the fence itself cannot be installed, an old unbound process must not
// continue using credentials whose replacement could not be guarded. Stop the
// affected managed cores with a fresh timeout, even if the apply was cancelled.
// This exceptional fallback can affect peers sharing a sing-box process.
func (a *Agent) stopBindingCores(ds *agentproto.DesiredState) error {
	names := map[string]bool{}
	if len(ds.Forwards) > 0 {
		names["singbox"] = true
	}
	for _, n := range ds.Nodes {
		if n.Network != nil {
			names[n.Core] = true
		}
	}
	if a.bindings != nil && a.bindings.plan != nil {
		for _, b := range a.bindings.plan.Bindings {
			names[b.Core] = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var errs []error
	for _, name := range []string{"singbox", "snell", "mieru"} {
		if !names[name] {
			continue
		}
		driver := a.Drivers[name]
		if driver == nil {
			errs = append(errs, fmt.Errorf("network protection: missing %s driver", name))
			continue
		}
		if err := driver.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("network protection: stop %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
