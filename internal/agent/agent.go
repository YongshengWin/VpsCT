package agent

import (
	"context"
	"crypto/rand"
	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentwork"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/conntail"
	"ctlvps/internal/core"
	"ctlvps/internal/diag"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/nft"
	"ctlvps/internal/proxyguard"
	"ctlvps/internal/secureupdate"
)

// Agent is the long-running process.
type Agent struct {
	StateDir string
	State    *State
	Client   *Client
	Logger   *slog.Logger
	Version  string

	Paths       core.Paths
	Systemd     *core.Systemd
	NFT         *nft.Manager
	Drivers     map[string]core.Driver
	Metrics     *MetricsCollector
	Tail        *conntail.Tailer
	PrivateTail *conntail.Tailer

	retirementHost      RetirementHost
	finalMeters         bool
	networkVersion      int
	billingPendingSaved bool
	billingError        string
	network             *netinventory.Collector
	bindings            *bindingRuntime
	networkCache        *networkDesiredCache // latest accepted remote intent; guarded by stateMu
	networkRetryAt      time.Time
	HoldUpdates         bool // locally pin a canary; pauses binary synchronization and web maintenance

	stateMu     sync.Mutex // serializes the main loop and connlog state persistence
	mu          sync.Mutex
	desired     *agentproto.DesiredState
	lastDiag    diag.Host
	lastDiagAt  time.Time
	clockSkewMs int64
	applying    bool
	bootID      string
	selfSHA     string
}

// New wires the agent for a state directory.
func New(stateDir string, st *State, logger *slog.Logger, version string) *Agent {
	debug.SetMemoryLimit(agentbudget.ResidentGoBytes)
	paths := core.DefaultPaths(stateDir)
	sd := core.NewSystemd()
	a := &Agent{
		StateDir: stateDir, State: st, Logger: logger, Version: version,
		Client: NewClient(st.ServerURL, st.AgentToken, version),
		Paths:  paths, Systemd: sd, NFT: nft.New(),
		Metrics: NewMetricsCollector(),
		bootID:  BootID(),
	}
	a.retirementHost = retirementHost{a}
	a.network = netinventory.New(stateDir, a.bootID)
	a.billingPendingSaved = st.NetworkBillingPending != nil
	a.Drivers = map[string]core.Driver{
		"singbox": core.NewSingBox(paths, sd),
		"snell":   core.NewSnell(paths, sd),
		"mieru":   core.NewMita(paths, sd),
	}
	a.Tail = conntail.New(paths.LogPath())
	a.Tail.Enabled = func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.desired != nil && a.desired.Connlog.Enabled
	}
	if p := executablePath(); p != "" {
		if sum, err := fileSHA256(p); err == nil {
			a.selfSHA = sum
		}
	}
	a.Tail.Allowed = func(nodeID int64) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.desired == nil {
			return false
		}
		for _, n := range a.desired.Nodes {
			if n.NodeID == nodeID {
				return n.ConnlogEnabled
			}
		}
		return false
	}
	a.PrivateTail = conntail.New(filepath.Join(paths.LogDir, "sing-box-private.log"))
	a.PrivateTail.Enabled = a.Tail.Enabled
	a.PrivateTail.Allowed = a.Tail.Allowed
	queue := conntail.NewSharedQueue()
	a.Tail.Queue = queue
	a.PrivateTail.Queue = queue
	return a
}

// Enroll performs first-contact enrolment and writes the state file.
func Enroll(ctx context.Context, stateDir, serverURL, token, version string) (*State, error) {
	c := NewClient(serverURL, "", version)
	host, _ := os.Hostname()
	resp, err := c.Enroll(ctx, agentproto.EnrollRequest{EnrollToken: token, Version: version, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Kernel: kernelVersion()})
	if err != nil {
		return nil, err
	}
	st := &State{ServerURL: c.BaseURL, AgentToken: resp.AgentToken, ServerID: resp.ServerID, ServerName: resp.ServerName, PollIntervalSec: resp.PollIntervalSec, EnrolledAt: time.Now().UTC(), CounterNonce: nonce()}
	if err := st.Save(stateDir); err != nil {
		return nil, err
	}
	return st, nil
}

func kernelVersion() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return runtime.GOOS
	}
	return string(b)
}

func nonce() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Run executes the main loop until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.loadNetworkDesired(); err != nil && !os.IsNotExist(err) {
		a.Logger.Warn("network recovery cache unavailable", "err", err)
	}
	if a.network != nil {
		defer a.network.Close()
		a.bindings = newBindingRuntime(ctx, a.network)
		defer a.bindings.stop() // monitor must stop before its collector closes
		if a.maintenanceAction() == "" {
			release, err := lockConfiguration()
			if err == nil {
				err = a.bindings.restore(ctx)
				release()
			}
			if err != nil && !os.IsNotExist(err) {
				a.Logger.Warn("restore network protection", "err", err)
			}
		}
	}
	interval := time.Duration(max(10, min(300, a.State.PollIntervalSec))) * time.Second
	if interval <= 0 {
		interval = agentproto.DefaultPollIntervalSec * time.Second
	}
	if a.State.CounterNonce == "" {
		a.State.CounterNonce = nonce()
		_ = a.State.Save(a.StateDir)
	}
	// nft table missing (fresh boot) -> counters restart from zero: new epoch
	if a.NFT.Available(ctx) && ((a.State.MeteringV1 && !a.NFT.NodesExist(ctx)) || (!a.State.MeteringV1 && !a.NFT.Exists(ctx))) {
		a.State.CounterNonce = nonce()
		_ = a.State.Save(a.StateDir)
	}
	a.stateMu.Lock()
	go a.Tail.Run(ctx)
	go a.PrivateTail.Run(ctx)
	go a.connlogLoop(ctx)

	// always apply once on start so a self-update can rewrite units
	if a.maintenanceAction() != "uninstall" {
		a.converge(ctx, true)
	}
	a.stateMu.Unlock()
	refreshCtx, cancelRefresh := context.WithCancel(ctx)
	refreshDone := make(chan struct{})
	go func() { defer close(refreshDone); a.networkRefreshLoop(refreshCtx) }()
	defer func() { cancelRefresh(); <-refreshDone }()
	t := time.NewTicker(interval)
	defer t.Stop()
	backoff := time.Second
	for {
		a.stateMu.Lock()
		err := a.heartbeat(ctx)
		a.stateMu.Unlock()
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				a.Logger.Error("token rejected; waiting for re-enrolment", "err", err)
			} else {
				a.Logger.Warn("heartbeat failed", "err", err)
			}
			backoff = min(backoff*2, 5*time.Minute)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}
		backoff = time.Second
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// epoch identifies the current counter baseline.
func (a *Agent) epoch() string { return a.bootID + ":" + a.State.CounterNonce }

// portCounters includes retired identities until their final counters are
// acknowledged. Accounting lives outside the service lifetime.
func (a *Agent) portCounters(ctx context.Context) ([]agentproto.PortCounter, error) {
	var out []agentproto.PortCounter
	if a.NFT.NodesExist(ctx) {
		counters, err := a.NFT.ReadNodes(ctx)
		if err != nil {
			return nil, err
		}
		for j := range counters {
			counters[j].Epoch = a.epoch() + ":nft-node-v1"
			for _, n := range a.State.MeterNodes {
				if n.NodeID == counters[j].NodeID {
					counters[j].Epoch = a.meterEpoch(n)
					break
				}
			}
		}
		out = append(out, counters...)
	} else if a.State.MeteringV1 {
		return nil, fmt.Errorf("node accounting table missing")
	}
	units := []string{}
	byUnit := map[string]MeterIdentity{}
	for _, n := range a.State.MeterNodes {
		if !core.IsStandalone(n.Core) {
			continue
		}
		u := core.SnellSlice(n.NodeID)
		if !a.Systemd.IsActive(ctx, u) {
			if a.State.MeteringV1 && a.Systemd.IsActive(ctx, core.StandaloneUnit(n.Core, n.Port)) {
				return nil, fmt.Errorf("Snell accounting slice unavailable")
			}
			continue
		}
		units = append(units, u)
		byUnit[u] = n
	}
	readings, err := a.Systemd.AccountingSnapshot(ctx, units)
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		r := readings[u]
		if !r.Valid {
			return nil, fmt.Errorf("accounting unavailable for %s", u)
		}
		n := byUnit[u]
		out = append(out, agentproto.PortCounter{NodeID: n.NodeID, Port: n.Port, Source: "systemd-v1", Epoch: r.Epoch, Rx: r.Rx, Tx: r.Tx, FromZero: true})
	}
	if !a.State.MeteringV1 && a.State.LegacySettled {
		for _, n := range a.State.MeterNodes {
			u := core.SingBoxUnit(n.Port)
			if core.IsStandalone(n.Core) {
				u = core.StandaloneUnit(n.Core, n.Port)
				if a.Systemd.InSlice(ctx, u, core.SnellSlice(n.NodeID)) {
					continue
				}
			}
			if !a.Systemd.IsActive(ctx, u) {
				continue
			}
			r, e := a.Systemd.AccountingSnapshot(ctx, []string{u})
			if e != nil {
				return nil, e
			}
			v := r[u]
			if !v.Valid {
				return nil, fmt.Errorf("legacy accounting unavailable")
			}
			out = append(out, agentproto.PortCounter{NodeID: n.NodeID, Port: n.Port, Source: "systemd-legacy-v1", Epoch: v.Epoch, Rx: v.Rx, Tx: v.Tx, FromZero: true})
		}
	}
	return out, nil
}

// flushSettlement retries the exact durable snapshot, never a newly sampled
// value. Baselines and usage are committed together by the controller.
func (a *Agent) flushSettlement(ctx context.Context) error {
	if a.State.PendingSettlement == nil {
		return nil
	}
	resp, err := a.Client.Heartbeat(ctx, *a.State.PendingSettlement)
	if err != nil {
		return fmt.Errorf("legacy meter settlement: %w", err)
	}
	if resp.MeteringVersion < 1 {
		return fmt.Errorf("upgrade controller before migrating node accounting")
	}
	pending := a.State.PendingSettlement
	a.State.PendingSettlement = nil
	a.State.LegacySettled = true
	if err := a.State.Save(a.StateDir); err != nil {
		a.State.PendingSettlement = pending
		return err
	}
	return nil
}

// prepareMetering stops legacy services before reading their retained final
// counters. Failure restores their availability; a durable report survives a
// lost acknowledgment and prevents double billing on retry.
func (a *Agent) prepareMetering(ctx context.Context, ds *agentproto.DesiredState) (func(bool), error) {
	if err := a.flushSettlement(ctx); err != nil {
		return nil, err
	}
	if a.State.MeteringV1 {
		return func(bool) {}, nil
	}
	identities := map[string]agentproto.NodeSpec{}
	for _, n := range ds.Nodes {
		u := core.SingBoxUnit(n.ListenPort)
		if core.IsStandalone(n.Core) {
			u = core.StandaloneUnit(n.Core, n.ListenPort)
		}
		identities[u] = n
	}
	for _, n := range a.State.MeterNodes {
		u := core.SingBoxUnit(n.Port)
		if core.IsStandalone(n.Core) {
			u = core.StandaloneUnit(n.Core, n.Port)
		}
		if _, ok := identities[u]; !ok {
			identities[u] = agentproto.NodeSpec{NodeID: n.NodeID, ListenPort: n.Port, Core: n.Core}
		}
	}
	for _, pattern := range []string{"ctlvps-singbox@*.service", "ctlvps-snell@*.service", "ctlvps-mita@*.service"} {
		for _, u := range a.Systemd.ListUnits(ctx, pattern) {
			if _, ok := identities[u]; !ok && a.Systemd.IsActive(ctx, u) {
				return nil, fmt.Errorf("cannot settle unknown legacy service %s; reconcile its node identity first", u)
			}
		}
	}
	active := []string{}
	guardedMigration := false
	for u := range identities {
		guardedMigration = guardedMigration || identities[u].Network != nil
		if n := identities[u]; core.IsStandalone(n.Core) && a.Systemd.InSlice(ctx, u, core.SnellSlice(n.NodeID)) {
			continue
		}
		if a.Systemd.IsActive(ctx, u) {
			active = append(active, u)
		}
	}
	restore := func(success bool) {
		// Legacy processes do not carry the new node marks. Once a network
		// policy is requested, restarting them can bypass the binding fence,
		// including after a settlement or nft failure. Keep them stopped.
		if success || guardedMigration {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// No legacy process may run under the new sing-box counters: its
		// process counters already include the same client packets.
		if !a.Systemd.IsActive(c, "ctlvps-singbox.service") && a.NFT.NodesExist(c) {
			if err := a.NFT.EnsureNodes(c, nil); err != nil {
				a.Logger.Error("restore accounting rules", "err", err)
				return
			}
		}
		for _, u := range active {
			if strings.HasPrefix(u, "ctlvps-singbox@") && a.Systemd.IsActive(c, "ctlvps-singbox.service") {
				continue
			}
			_ = a.Systemd.StartUnits(c, []string{u})
		}
	}
	if err := a.Systemd.StopUnits(ctx, active); err != nil {
		restore(false)
		return nil, err
	}
	readings, err := a.Systemd.AccountingSnapshot(ctx, active)
	if err != nil {
		restore(false)
		return nil, err
	}
	hb := agentproto.Heartbeat{Version: a.Version, Epoch: a.epoch(), TS: time.Now().UTC(), Metrics: a.Metrics.Collect()}
	for _, u := range active {
		n, r := identities[u], readings[u]
		if !r.Valid {
			restore(false)
			return nil, fmt.Errorf("legacy accounting unavailable for %s", u)
		}
		if !a.State.LegacySettled {
			hb.Ports = append(hb.Ports, agentproto.PortCounter{Port: n.ListenPort, Rx: r.Rx, Tx: r.Tx})
		}
		hb.Ports = append(hb.Ports, agentproto.PortCounter{NodeID: n.NodeID, Port: n.ListenPort, Source: "systemd-legacy-v1", Epoch: r.Epoch, Rx: r.Rx, Tx: r.Tx, FromZero: a.State.LegacySettled})
	}
	a.State.PendingSettlement = &hb
	if err = a.State.Save(a.StateDir); err != nil {
		restore(false)
		return nil, err
	}
	if err = a.flushSettlement(ctx); err != nil {
		restore(false)
		return nil, err
	}
	return restore, nil
}

func (a *Agent) heartbeat(ctx context.Context) error {
	if err := a.flushForwardReceipt(ctx); err != nil {
		return err
	}
	billingErr := a.flushNetworkBilling(ctx)
	retirementErr := a.flushRetirement(ctx)
	if err := a.flushSettlement(ctx); err != nil {
		return err
	}
	hb := agentproto.Heartbeat{Version: a.Version, BinarySHA256: a.selfSHA, Epoch: a.epoch(), TS: time.Now().UTC(), Metrics: a.Metrics.Collect()}
	if a.networkVersion >= agentproto.NetworkVersion && a.network != nil {
		a.billingPreferences()
		hb.Metrics.Network = a.network.Collect()
	}
	hb.PublicIPv4, hb.PublicIPv6 = diag.PublicIPs()
	var meterErr error
	hb.Ports, meterErr = a.portCounters(ctx)
	if p := a.State.Retirement; p != nil {
		pending := map[int64]bool{}
		for _, n := range p.Nodes {
			pending[n.NodeID] = true
		}
		kept := hb.Ports[:0]
		for _, c := range hb.Ports {
			if !pending[c.NodeID] {
				kept = append(kept, c)
			}
		}
		hb.Ports = kept
	}
	if meterErr != nil {
		hb.Ports = nil
	}
	if a.State.NetworkForwardVersion > 0 {
		var err error
		hb.ForwardCounters, err = a.forwardCounters(ctx)
		if err != nil {
			meterErr = errors.Join(meterErr, err)
		}
	}
	a.mu.Lock()
	hb.AppliedRevision, hb.AppliedHash, hb.ApplyError = a.State.AppliedRevision, a.State.AppliedHash, a.State.ApplyError
	if agentwork.Pending() {
		hb.ApplyStatus = "pending"
	} else if hb.ApplyError != "" {
		hb.ApplyStatus = "failed"
	} else if hb.AppliedRevision > 0 {
		hb.ApplyStatus = "applied"
	} else {
		hb.ApplyStatus = "pending"
	}
	a.mu.Unlock()
	hb.Diagnostics = a.diagnostics(ctx)
	a.addNetworkBillingReport(&hb)
	if billingErr != nil {
		hb.Diagnostics.NetworkBillingError = billingErr.Error()
	}
	if len(hb.Diagnostics.NetworkBillingError) > 512 {
		hb.Diagnostics.NetworkBillingError = "计费来源切换尚未完成，请查看 agent 日志"
	}
	if retirementErr != nil {
		hb.Diagnostics.MeteringError = "final meter settlement pending"
	}
	if meterErr != nil {
		a.State.ApplyError = "node metering unavailable"
		hb.ApplyError, hb.ApplyStatus = a.State.ApplyError, "failed"
		hb.Diagnostics.MeteringError = meterErr.Error()
	}

	sent := time.Now()
	resp, err := a.Client.Heartbeat(ctx, hb)
	if err != nil {
		// Re-negotiate after failure, including a controller rollback which
		// rejects the previously advertised optional extension.
		a.networkVersion = 0
		return err
	}
	a.finalMeters = resp.FinalMeterVersion >= 1
	a.networkVersion = resp.NetworkVersion
	if !resp.ServerTime.IsZero() {
		rtt := time.Since(sent)
		a.mu.Lock()
		a.clockSkewMs = resp.ServerTime.Add(rtt / 2).Sub(time.Now()).Milliseconds()
		a.mu.Unlock()
	}
	resp.PollIntervalSec = max(10, min(300, resp.PollIntervalSec))
	if resp.PollIntervalSec > 0 && resp.PollIntervalSec != a.State.PollIntervalSec {
		a.State.PollIntervalSec = resp.PollIntervalSec
		_ = a.State.Save(a.StateDir)
	}
	if err := a.receiveNetworkBilling(ctx, resp); err != nil {
		a.billingError = err.Error()
		a.Logger.Warn("network billing switch pending", "err", err)
	} else {
		a.billingError = ""
	}
	if retirementErr != nil {
		a.Logger.Warn("final settlement pending", "err", retirementErr)
		return nil
	}
	if resp.Maintenance != nil {
		if agentwork.Pending() {
			return nil
		}
		return a.maintain(ctx, *resp.Maintenance)
	}
	if a.maintenanceAction() != "" {
		return nil
	}
	if !a.HoldUpdates && resp.AgentUpdate != nil && resp.AgentUpdate.SHA256 != "" && !strings.EqualFold(resp.AgentUpdate.SHA256, a.selfSHA) {
		a.Logger.Info("self-update available", "sha", resp.AgentUpdate.SHA256[:min(12, len(resp.AgentUpdate.SHA256))])
		if err := applySelfUpdate(ctx, a.State.ServerURL, *resp.AgentUpdate, a.StateDir); err != nil {
			if !errors.Is(err, agentwork.ErrPending) {
				a.Logger.Error("self-update failed", "err", err)
			}
		} else {
			a.Logger.Info("self-update installed; exiting for systemd restart")
			os.Exit(0)
		}
	}
	if pending, err := a.retirementPending(ctx); err != nil {
		return err
	} else if pending {
		return nil
	}
	a.mu.Lock()
	needInitialApply := a.desired == nil
	currentDesired := a.desired
	a.mu.Unlock()
	if policy, err := secureupdate.LoadPolicy(); currentDesired != nil && err == nil && !policy.PauseConfig && secureupdate.Allow("agent.configure") == nil {
		if release, err := lockConfiguration(); err == nil {
			if err := a.NFT.EnsureResourceIngress(ctx, currentDesired.Nodes, currentDesired.Forwards); err != nil {
				a.State.ApplyError = "节点端口开放失败: " + err.Error()
			}
			release()
		}
	}
	if needInitialApply || resp.DesiredRevision != a.State.AppliedRevision || resp.DesiredHash != a.State.AppliedHash || a.State.ApplyError != "" || a.bindings.needsApply() {
		a.converge(ctx, false)
	}
	return nil
}

func (a *Agent) diagnostics(ctx context.Context) agentproto.Diagnostics {
	a.mu.Lock()
	needHost := time.Since(a.lastDiagAt) > 5*time.Minute
	skew := a.clockSkewMs
	ds := a.desired
	a.mu.Unlock()
	if needHost {
		h := diag.Collect(ctx)
		a.mu.Lock()
		a.lastDiag, a.lastDiagAt = h, time.Now()
		a.mu.Unlock()
	}
	a.mu.Lock()
	h := a.lastDiag
	a.mu.Unlock()
	d := agentproto.Diagnostics{
		ClockSkewMs: skew, BBR: h.BBR, CongestionCtl: h.CongestionCtl, IPv6Reachable: h.IPv6Reachable, IPv4Reachable: h.IPv4Reachable,
		OOMEvents: h.OOMEvents, Nftables: h.Nftables, Systemd: h.Systemd, TimeSync: h.TimeSync, Warnings: h.Warnings,
		BinarySHA256: a.selfSHA,
	}
	d.SecurityVersion = 1
	if a.State.MeteringV1 {
		d.MeterInventoryVersion = 1
		for _, identity := range a.State.MeterNodes {
			d.RetainedNodeMeters = append(d.RetainedNodeMeters, identity.NodeID)
		}
	}
	d.NetworkBillingError = a.billingError
	d.NetworkBindingErrors, d.NetworkGuardError = a.bindings.status()
	d.NetworkForwardErrors = a.bindings.forwardStatus()
	if runtime.GOOS == "linux" {
		d.NetworkBillingVersion = agentproto.NetworkBillingVersion
		d.NetworkBindingVersion = agentproto.NetworkBindingVersion
		d.ListenBindingVersion = 1
		d.ForwardDNSVersion = 1
		d.ForwardPrivateVersion = 1
		d.NetworkEgressVersion = agentproto.NetworkEgressVersion
		d.MitaVersion = networkconfig.MitaVersion
		d.NetworkSSHVersion = agentproto.NetworkSSHVersion
		d.NetworkWireGuardVersion = agentproto.NetworkWireGuardVersion
		d.NetworkForwardVersion = agentproto.NetworkForwardVersion
		d.ForwardTransportVersion = 1
		d.NetworkTransportVersion = agentproto.NetworkTransportVersion
	}
	if policy, e := secureupdate.LoadPolicy(); e == nil {
		if !policy.ChecksumOnly {
			d.Warnings = append(append([]string(nil), d.Warnings...), secureupdate.TrustWarnings(secureupdate.StateDir, time.Now())...)
		}
		d.SecurityPolicy = true
		d.SecurityPaused = policy.PauseConfig
		d.NetworkConfigureAllowed = runtime.GOOS == "linux" && !policy.PauseConfig && secureupdate.Allow("agent.configure") == nil
		if runtime.GOOS == "linux" {
			d.TransportGrants = policy.TransportGrants
			d.ForwardGrants = policy.ForwardGrants
		}
	}
	if a.maintenanceSupported() {
		d.Maintenance = 1
	}
	wanted := map[string]bool{}
	if ds != nil {
		for _, n := range ds.Nodes {
			if !n.Blocked {
				wanted[n.Core] = true
			}
		}
	}
	if ds != nil {
		for _, f := range ds.Forwards {
			if !f.Blocked && !f.Retired {
				wanted["singbox"] = true
			}
		}
	}
	for name, drv := range a.Drivers {
		st := drv.Status(ctx)
		st.Wanted = wanted[name]
		d.Cores = append(d.Cores, st)
	}
	if sb, ok := a.Drivers["singbox"].(*core.SingBox); ok && ds != nil {
		d.Certs = sb.Certs(ds.Nodes)
		d.RecentErrors = sb.RecentErrors(5)
	}
	pending, _ := a.Tail.Pending()
	if a.PrivateTail != nil && (a.Tail.Queue == nil || a.PrivateTail.Queue != a.Tail.Queue) {
		p, _ := a.PrivateTail.Pending()
		pending += p
	}
	d.ConnlogLag = int64(pending)
	return d
}

// converge fetches the desired state and applies it, reporting the result.
func (a *Agent) converge(ctx context.Context, force bool) {
	a.convergeDesired(ctx, force, nil)
}

// A local refresh reuses only the exact durable accepted intent. It repeats all
// local authorization/application checks, but does not fetch or report HTTP.
func (a *Agent) convergeDesired(ctx context.Context, force bool, local *agentproto.DesiredState) {
	if a.State.ForwardPending != nil {
		return
	}
	if a.maintenanceAction() != "" {
		return
	}
	if local != nil {
		if !a.State.MeteringV1 || a.State.Retirement != nil || a.State.PendingSettlement != nil || agentwork.Pending() {
			return
		}
		if err := validateNetworkRecovery(local, a.State); err != nil {
			return
		}
	} else if pending, err := a.retirementPending(ctx); err != nil {
		a.Logger.Warn("meter retirement", "err", err)
		return
	} else if pending {
		return
	}
	a.mu.Lock()
	if a.applying {
		a.mu.Unlock()
		return
	}
	a.applying = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.applying = false
		a.mu.Unlock()
	}()

	if p, e := secureupdate.LoadPolicy(); e != nil || p.PauseConfig {
		a.Logger.Warn("本机安全策略未就绪或已暂停配置变更")
		return
	}
	ds := local
	var err error
	if ds == nil {
		ds, err = a.Client.Desired(ctx)
		if err != nil {
			a.Logger.Warn("fetch desired state", "err", err)
			return
		}
	}
	lastRevision, lastHash := a.State.desiredBoundary()
	if err := agentproto.ValidateDesired(ds, a.State.ServerID, lastRevision, lastHash); err != nil {
		a.Logger.Warn("rejected desired state", "err", err)
		return
	}
	// Capture the remote representation before local certificate paths/private
	// permissions are resolved. Those local decisions must be reevaluated later.
	var recovery []byte
	if ds.NetworkEgressVersion > 0 || ds.NetworkForwardVersion > 0 {
		recovery, err = json.Marshal(ds)
		if err != nil || len(recovery) > agentbudget.ConfigBytes {
			return
		}
	}
	policy, _ := secureupdate.LoadPolicy()
	if e := secureupdate.Allow("agent.configure"); e != nil {
		a.Logger.Warn("configuration not permitted by local policy")
		return
	}
	maxNodes := policy.MaxNodes
	if maxNodes <= 0 {
		maxNodes = agentbudget.ActiveNodes
	}
	maxNodes = min(maxNodes, agentbudget.ActiveNodes)
	maxMemory := policy.MaxMemoryMB
	if maxMemory <= 0 {
		maxMemory = 512
	}
	if len(ds.Nodes) > maxNodes || ds.Tuning.MemoryMaxMB > maxMemory || ds.Tuning.GoMemLimitMB > maxMemory {
		a.Logger.Warn("configuration exceeds local resource policy")
		return
	}
	for i := range ds.Forwards {
		ds.Forwards[i].ForwardGrants = networkconfig.ForwardGrantsFor(policy.ForwardGrants, ds.Forwards[i].ForwardID, ds.Forwards[i].Config.EgressProfileID)
	}
	for i := range ds.Nodes {
		if network := ds.Nodes[i].Network; network != nil && network.HasTransport() {
			ds.Nodes[i].TransportGrants = networkconfig.TransportGrantsFor(policy.TransportGrants, ds.Nodes[i].NodeID, network.Policy.EgressProfileID)
		}
		for _, id := range policy.PrivateNodes {
			if ds.Nodes[i].NodeID == id {
				ds.Nodes[i].AllowPrivate = true
			}
		}
		c := ds.Nodes[i].Cert
		if c != nil && c.Mode == "acme" {
			allowed := policy.ChecksumOnly && len(policy.ACMEDomains) == 0
			for _, domain := range policy.ACMEDomains {
				if domain == c.Domain {
					allowed = true
				}
			}
			if !allowed {
				a.Logger.Warn("ACME domain not locally authorized")
				return
			}
		}
		if c != nil && c.Mode == "external" {
			p, e := secureupdate.LoadPolicy()
			if e != nil {
				a.Logger.Warn("external certificate policy unavailable")
				return
			}
			cert, ok := p.Certificates[c.ID]
			if !ok || c.ID == "" {
				a.Logger.Warn("external certificate not registered")
				return
			}
			c.CertPath, c.KeyPath = cert.Cert, cert.Key
		}
	}
	release, err := lockConfiguration()
	if err != nil {
		a.Logger.Warn("configuration deferred while the agent target is busy or unavailable")
		return
	}
	defer release()
	if err := a.rememberNetworkIntent(ds); err != nil {
		a.Logger.Warn("persist network application intent", "err", err)
		return
	}
	if len(recovery) > 0 {
		if err := a.saveNetworkDesired(recovery); err != nil {
			a.Logger.Warn("persist network recovery intent", "err", err)
			if stopErr := a.stopBindingCores(ds); stopErr != nil {
				a.Logger.Error("stop after network recovery persistence failure", "err", stopErr)
			}
			return
		}
	}
	a.mu.Lock()
	hasDesired := a.desired != nil
	a.mu.Unlock()
	if !force && hasDesired && ds.Revision == a.State.AppliedRevision && ds.Hash == a.State.AppliedHash && a.State.ApplyError == "" && !a.bindings.needsApply() && proxyguard.Ready() == nil {
		return
	}
	a.Logger.Info("applying desired state", "revision", ds.Revision, "nodes", len(ds.Nodes))
	details, err := a.apply(ctx, ds)
	rep := agentproto.ApplyReport{Revision: ds.Revision, Hash: ds.Hash, Status: "applied", Details: details}
	if err != nil {
		rep.Status, rep.Error = "failed", err.Error()
		if errors.Is(err, agentwork.ErrPending) {
			rep.Status = "pending"
		}
		a.State.ApplyError = err.Error()
		if !errors.Is(err, agentwork.ErrPending) {
			a.Logger.Error("apply failed", "revision", ds.Revision, "err", err)
		}
	} else {
		a.State.AppliedRevision, a.State.AppliedHash = ds.Revision, ds.Hash
		a.State.ApplyError = ""
		a.mu.Lock()
		a.desired = ds
		a.mu.Unlock()
		a.Logger.Info("applied", "revision", ds.Revision, "details", details)
	}
	_ = a.State.Save(a.StateDir)
	if local != nil {
		return
	} // next heartbeat reports the local apply result
	if err := a.Client.Report(ctx, rep); err != nil {
		a.Logger.Warn("report failed", "err", err)
	}
}

// apply converges the host. It keeps going after per-core failures so one
// broken core does not take the others down, and returns a joined error.
func (a *Agent) apply(ctx context.Context, ds *agentproto.DesiredState) ([]string, error) {
	var details []string
	var errs []error
	note := func(format string, args ...any) { details = append(details, fmt.Sprintf(format, args...)) }

	// Persist meter identities before any process can start producing bytes.
	if err := a.State.rememberMeters(ds.Nodes); err != nil {
		return nil, err
	}
	if err := a.State.Save(a.StateDir); err != nil {
		return details, err
	}

	restore, err := a.prepareMetering(ctx, ds)
	if err != nil {
		return details, err
	}
	success := false
	defer func() { restore(success) }()
	if !a.NFT.Available(ctx) {
		return details, fmt.Errorf("nftables is required for shared-process node accounting")
	}
	if !a.NFT.NodesExist(ctx) {
		a.State.CounterNonce = nonce()
		if err := a.State.Save(a.StateDir); err != nil {
			return details, err
		}
	}
	meterNodes := append([]agentproto.NodeSpec(nil), ds.Nodes...)
	current := map[int64]bool{}
	for _, n := range ds.Nodes {
		current[n.NodeID] = true
	}
	for _, n := range a.State.MeterNodes {
		if n.Core == "singbox" && !current[n.NodeID] {
			meterNodes = append(meterNodes, agentproto.NodeSpec{NodeID: n.NodeID, Core: n.Core, Blocked: true, Retired: true})
		}
	}
	if err := a.NFT.EnsureNodes(ctx, meterNodes); err != nil {
		return details, fmt.Errorf("node accounting: %w", err)
	}

	if err := a.prepareForwardMeters(ctx, ds); err != nil {
		return details, err
	}
	if err := a.Systemd.EnsureProxyBudget(ctx, ds.Tuning); err != nil {
		return details, err
	}

	groups := map[int64]string{}
	for _, n := range ds.Nodes {
		if n.Core == "singbox" {
			g, e := a.Systemd.EnsureSingBoxSlice(ctx, n.AllowPrivate)
			if e != nil {
				return details, e
			}
			groups[n.NodeID] = g
		}
		if core.IsStandalone(n.Core) {
			if _, e := a.Systemd.EnsureSnellMeter(ctx, n); e != nil {
				return details, e
			}
			g, e := a.Systemd.ControlGroup(ctx, core.SnellSlice(n.NodeID))
			if e != nil {
				return details, e
			}
			groups[n.NodeID] = g
		}
	}
	forwardGroup := ""
	if ds.NetworkForwardVersion > 0 {
		forwardGroup, err = a.Systemd.EnsureSingBoxSlice(ctx, false)
		if err != nil {
			return details, err
		}
	}
	if e := a.NFT.EnsureResourceEgress(ctx, ds.Nodes, groups, ds.Forwards, forwardGroup); e != nil {
		return details, fmt.Errorf("proxy egress protection unavailable: %w", e)
	}
	// Quota/retirement and root egress policy have already been enforced.
	// A missing interface must not short-circuit those restrictions.
	coreNodes := ds.Nodes
	var coreForwards []agentproto.ForwardSpec
	var bindingTx *bindingApply
	if a.bindings != nil {
		bindingTx, err = a.bindings.prepare(ctx, ds, groups)
		if err != nil {
			return details, errors.Join(fmt.Errorf("network protection: %w", err), a.stopBindingCores(ds))
		}
		coreNodes, coreForwards = bindingTx.nodes, bindingTx.forwards
		errs = append(errs, bindingTx.failures...)
	} else {
		if ds.NetworkForwardVersion > 0 {
			return details, errors.New("forward binding runtime unavailable")
		}
		for _, n := range ds.Nodes {
			if n.Network != nil {
				return details, errors.New("network binding runtime unavailable")
			}
		}
	}

	// host tuning
	if ds.Tuning.EnableBBR {
		if changed, err := diag.EnableBBR(ctx); err != nil {
			note("bbr: %v", err)
		} else if changed {
			note("bbr enabled")
		}
	}
	if ds.Tuning.Chrony {
		diag.EnsureChrony(ctx)
	}

	// group nodes per core
	byCore := map[string][]agentproto.NodeSpec{}
	for _, n := range coreNodes {
		byCore[n.Core] = append(byCore[n.Core], n)
	}
	appliedCores := map[string]bool{}
	for name, drv := range a.Drivers {
		nodes := byCore[name]
		live := 0
		for _, n := range nodes {
			if !n.Blocked {
				live++
			}
		}
		if name == "singbox" {
			live += len(coreForwards)
		}
		if live > 0 {
			key := map[string]string{"singbox": "sing-box", "snell": "snell-server", "mieru": "mita"}[name]
			v, ok := ds.Versions[key]
			if !ok {
				errs = append(errs, fmt.Errorf("missing trusted core version"))
				continue
			}
			{
				if changed, err := drv.EnsureInstalled(ctx, v); err != nil {
					errs = append(errs, err)
					note("%s: install failed: %v", name, err)
					continue
				} else if changed {
					note("%s: installed %s", name, v.Version)
				}
			}
		}
		var changed bool
		var err error
		if managed, ok := drv.(core.ResourceDriver); ok && name == "singbox" {
			changed, err = managed.ApplyResources(ctx, ds, nodes, coreForwards)
		} else if name == "singbox" && len(coreForwards) > 0 {
			err = errors.New("shared core lacks forwarding driver")
		} else {
			changed, err = drv.Apply(ctx, ds, nodes)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			note("%s: apply failed: %v", name, err)
			continue
		}
		if changed {
			note("%s: %d node(s) applied", name, live)
		}
		appliedCores[name] = true
	}
	if bindingTx != nil {
		if err := bindingTx.finish(ctx, appliedCores); err != nil {
			errs = append(errs, fmt.Errorf("network activation: %w", err), a.stopBindingCores(ds))
		}
	}
	if len(errs) == 0 {
		if err := a.NFT.EnsureResourceIngress(ctx, ds.Nodes, ds.Forwards); err != nil {
			errs = append(errs, fmt.Errorf("节点端口开放失败: %w", err))
		}
	}
	if len(errs) == 0 && a.NFT.Exists(ctx) {
		if err := a.NFT.Teardown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("remove settled legacy counters: %w", err))
		}
	}
	if len(errs) == 0 {
		a.State.MeteringV1 = true
		if err := a.State.Save(a.StateDir); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) == 0 {
		if err := a.freezeForwardReceipt(ctx, ds); err != nil {
			errs = append(errs, err)
		}
	}
	success = len(errs) == 0
	return details, errors.Join(errs...)
}

// connlogLoop uploads buffered connection events.
func (a *Agent) connlogLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var lastFlush time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.mu.Lock()
		ds := a.desired
		a.mu.Unlock()
		if ds == nil || !ds.Connlog.Enabled {
			continue
		}
		batch := ds.Connlog.BatchSize
		if batch <= 0 {
			batch = 500
		}
		batch = min(batch, 500)
		flush := time.Duration(ds.Connlog.FlushSec) * time.Second
		if flush <= 0 {
			flush = 30 * time.Second
		}
		pending, _ := a.Tail.Pending()
		if a.PrivateTail != nil && (a.Tail.Queue == nil || a.PrivateTail.Queue != a.Tail.Queue) {
			p, _ := a.PrivateTail.Pending()
			pending += p
		}
		if pending == 0 || (pending < batch && time.Since(lastFlush) < flush) {
			continue
		}
		events := a.Tail.Take(max(1, batch/2))
		if a.PrivateTail != nil && (a.Tail.Queue == nil || a.PrivateTail.Queue != a.Tail.Queue) && len(events) < batch {
			events = append(events, a.PrivateTail.Take(batch-len(events))...)
		}
		if len(events) < batch {
			events = append(events, a.Tail.Take(batch-len(events))...)
		}
		a.stateMu.Lock()
		seq := a.State.ConnlogSeq + 1
		a.stateMu.Unlock()
		ack, err := a.Client.UploadConnlog(ctx, agentproto.ConnlogBatch{Seq: seq, Events: events})
		if err != nil {
			a.Tail.Requeue(events)
			a.Logger.Warn("connlog upload failed", "err", err)
			continue
		}
		a.stateMu.Lock()
		a.State.ConnlogSeq = max(seq, ack.AcceptedSeq)
		_ = a.State.Save(a.StateDir)
		a.stateMu.Unlock()
		lastFlush = time.Now()
	}
}

// StatusSummary is printed by `ctlvps-agent status`.
func (a *Agent) StatusSummary(ctx context.Context) string {
	var b string
	b += fmt.Sprintf("server: %s (%s, id %d)\n", a.State.ServerURL, a.State.ServerName, a.State.ServerID)
	b += fmt.Sprintf("applied revision: %d  error: %q\n", a.State.AppliedRevision, a.State.ApplyError)
	for _, drv := range a.Drivers {
		st := drv.Status(ctx)
		b += fmt.Sprintf("core %-13s installed=%v version=%s active=%v restarts=%d cgroup-memory=%dMiB\n", st.Name, st.Installed, st.Version, st.Active, st.NRestarts, st.RSSBytes>>20)
	}
	counters, err := a.portCounters(ctx)
	if err != nil {
		b += "metering unavailable: " + err.Error() + "\n"
	}
	for _, c := range counters {
		b += fmt.Sprintf("port %-6d rx=%d tx=%d\n", c.Port, c.Rx, c.Tx)
	}
	return b
}
