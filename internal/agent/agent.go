package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/conntail"
	"ctlvps/internal/core"
	"ctlvps/internal/diag"
	"ctlvps/internal/nft"
)

// Agent is the long-running process.
type Agent struct {
	StateDir string
	State    *State
	Client   *Client
	Logger   *slog.Logger
	Version  string

	Paths   core.Paths
	Systemd *core.Systemd
	NFT     *nft.Manager
	Drivers map[string]core.Driver
	Metrics *MetricsCollector
	Tail    *conntail.Tailer

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
	paths := core.DefaultPaths(stateDir)
	sd := core.NewSystemd()
	a := &Agent{
		StateDir: stateDir, State: st, Logger: logger, Version: version,
		Client: NewClient(st.ServerURL, st.AgentToken, version),
		Paths:  paths, Systemd: sd, NFT: nft.New(),
		Metrics: NewMetricsCollector(),
		bootID:  BootID(),
	}
	a.Drivers = map[string]core.Driver{
		"singbox": core.NewSingBox(paths, sd),
		"snell":   core.NewSnell(paths, sd),
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
	interval := time.Duration(a.State.PollIntervalSec) * time.Second
	if interval <= 0 {
		interval = agentproto.DefaultPollIntervalSec * time.Second
	}
	if a.State.CounterNonce == "" {
		a.State.CounterNonce = nonce()
		_ = a.State.Save(a.StateDir)
	}
	// nft table missing (fresh boot) -> counters restart from zero: new epoch
	if a.NFT.Available(ctx) && !a.NFT.Exists(ctx) {
		a.State.CounterNonce = nonce()
		_ = a.State.Save(a.StateDir)
	}
	go a.Tail.Run(ctx)
	go a.connlogLoop(ctx)

	// always apply once on start so a self-update can rewrite units
	if a.maintenanceAction() != "uninstall" {
		a.converge(ctx, true)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	backoff := time.Second
	for {
		if err := a.heartbeat(ctx); err != nil {
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

// portCounters is VPS in/out per listen port.
// Prefers systemd IPAccounting (the process cgroup: client + origin).
// nft listen-port counters are only a fallback before the unit is ready.
func (a *Agent) portCounters(ctx context.Context) []agentproto.PortCounter {
	byPort := map[int]agentproto.PortCounter{}
	if a.NFT != nil && a.NFT.Available(ctx) {
		if counters, err := a.NFT.Read(ctx); err == nil {
			byPort = counters
		}
	}
	a.mu.Lock()
	ds := a.desired
	a.mu.Unlock()
	if ds != nil && a.Systemd != nil {
		for _, n := range ds.Nodes {
			if n.ListenPort <= 0 {
				continue
			}
			var unit string
			switch n.Core {
			case "snell":
				unit = core.SnellUnit(n.ListenPort)
			case "singbox":
				unit = core.SingBoxUnit(n.ListenPort)
			default:
				continue
			}
			in, out, ok := a.Systemd.IPAccounting(ctx, unit)
			if !ok {
				continue
			}
			pc := byPort[n.ListenPort]
			pc.Port = n.ListenPort
			pc.Rx, pc.Tx = in, out
			byPort[n.ListenPort] = pc
		}
	}
	out := make([]agentproto.PortCounter, 0, len(byPort))
	for _, c := range byPort {
		if c.Port > 0 {
			out = append(out, c)
		}
	}
	return out
}

func (a *Agent) heartbeat(ctx context.Context) error {
	hb := agentproto.Heartbeat{Version: a.Version, BinarySHA256: a.selfSHA, Epoch: a.epoch(), TS: time.Now().UTC(), Metrics: a.Metrics.Collect()}
	hb.PublicIPv4, hb.PublicIPv6 = diag.PublicIPs()
	hb.Ports = a.portCounters(ctx)
	a.mu.Lock()
	hb.AppliedRevision, hb.AppliedHash, hb.ApplyError = a.State.AppliedRevision, a.State.AppliedHash, a.State.ApplyError
	if hb.ApplyError != "" {
		hb.ApplyStatus = "failed"
	} else if hb.AppliedRevision > 0 {
		hb.ApplyStatus = "applied"
	} else {
		hb.ApplyStatus = "pending"
	}
	a.mu.Unlock()
	hb.Diagnostics = a.diagnostics(ctx)

	sent := time.Now()
	resp, err := a.Client.Heartbeat(ctx, hb)
	if err != nil {
		return err
	}
	if !resp.ServerTime.IsZero() {
		rtt := time.Since(sent)
		a.mu.Lock()
		a.clockSkewMs = resp.ServerTime.Add(rtt / 2).Sub(time.Now()).Milliseconds()
		a.mu.Unlock()
	}
	if resp.PollIntervalSec > 0 && resp.PollIntervalSec != a.State.PollIntervalSec {
		a.State.PollIntervalSec = resp.PollIntervalSec
		_ = a.State.Save(a.StateDir)
	}
	if resp.Maintenance != nil {
		return a.maintain(ctx, *resp.Maintenance)
	}
	if a.maintenanceAction() != "" {
		return nil
	}
	if resp.AgentUpdate != nil && resp.AgentUpdate.SHA256 != "" && !strings.EqualFold(resp.AgentUpdate.SHA256, a.selfSHA) {
		a.Logger.Info("self-update available", "sha", resp.AgentUpdate.SHA256[:min(12, len(resp.AgentUpdate.SHA256))])
		if err := applySelfUpdate(ctx, a.State.ServerURL, *resp.AgentUpdate); err != nil {
			a.Logger.Error("self-update failed", "err", err)
		} else {
			a.Logger.Info("self-update installed; exiting for systemd restart")
			os.Exit(0)
		}
	}
	if resp.DesiredRevision != a.State.AppliedRevision || resp.DesiredHash != a.State.AppliedHash || a.State.ApplyError != "" {
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
	d.ConnlogLag = int64(pending)
	return d
}

// converge fetches the desired state and applies it, reporting the result.
func (a *Agent) converge(ctx context.Context, force bool) {
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

	ds, err := a.Client.Desired(ctx)
	if err != nil {
		a.Logger.Warn("fetch desired state", "err", err)
		return
	}
	a.mu.Lock()
	a.desired = ds
	a.mu.Unlock()
	if !force && ds.Revision == a.State.AppliedRevision && ds.Hash == a.State.AppliedHash && a.State.ApplyError == "" {
		return
	}
	a.Logger.Info("applying desired state", "revision", ds.Revision, "nodes", len(ds.Nodes))
	details, err := a.apply(ctx, ds)
	rep := agentproto.ApplyReport{Revision: ds.Revision, Hash: ds.Hash, Status: "applied", Details: details}
	a.State.AppliedRevision, a.State.AppliedHash = ds.Revision, ds.Hash
	if err != nil {
		rep.Status, rep.Error = "failed", err.Error()
		a.State.ApplyError = err.Error()
		a.Logger.Error("apply failed", "revision", ds.Revision, "err", err)
	} else {
		a.State.ApplyError = ""
		a.Logger.Info("applied", "revision", ds.Revision, "details", details)
	}
	_ = a.State.Save(a.StateDir)
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
	var ports []int
	for _, n := range ds.Nodes {
		byCore[n.Core] = append(byCore[n.Core], n)
		ports = append(ports, n.ListenPort) // count blocked ports too (keep history)
	}
	for name, drv := range a.Drivers {
		nodes := byCore[name]
		live := 0
		for _, n := range nodes {
			if !n.Blocked {
				live++
			}
		}
		if live > 0 {
			key := map[string]string{"singbox": "sing-box", "snell": "snell-server"}[name]
			if v, ok := ds.Versions[key]; ok {
				if changed, err := drv.EnsureInstalled(ctx, v); err != nil {
					errs = append(errs, err)
					note("%s: install failed: %v", name, err)
					continue
				} else if changed {
					note("%s: installed %s", name, v.Version)
				}
			}
		}
		changed, err := drv.Apply(ctx, ds, nodes)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			note("%s: apply failed: %v", name, err)
			continue
		}
		if changed {
			note("%s: %d node(s) applied", name, live)
		}
	}
	// nftables counters
	if a.NFT.Available(ctx) {
		if err := a.NFT.Ensure(ctx, ports); err != nil {
			errs = append(errs, fmt.Errorf("nftables: %w", err))
			note("nftables: %v", err)
		}
	} else {
		note("nftables unavailable: per-port accounting disabled")
	}
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
		flush := time.Duration(ds.Connlog.FlushSec) * time.Second
		if flush <= 0 {
			flush = 30 * time.Second
		}
		pending, _ := a.Tail.Pending()
		if pending == 0 || (pending < batch && time.Since(lastFlush) < flush) {
			continue
		}
		events := a.Tail.Take(batch)
		seq := a.State.ConnlogSeq + 1
		ack, err := a.Client.UploadConnlog(ctx, agentproto.ConnlogBatch{Seq: seq, Events: events})
		if err != nil {
			a.Tail.Requeue(events)
			a.Logger.Warn("connlog upload failed", "err", err)
			continue
		}
		a.State.ConnlogSeq = max(seq, ack.AcceptedSeq)
		_ = a.State.Save(a.StateDir)
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
		b += fmt.Sprintf("core %-13s installed=%v version=%s active=%v restarts=%d rss=%dMiB\n", st.Name, st.Installed, st.Version, st.Active, st.NRestarts, st.RSSBytes>>20)
	}
	for _, c := range a.portCounters(ctx) {
		b += fmt.Sprintf("port %-6d rx=%d tx=%d\n", c.Port, c.Rx, c.Tx)
	}
	return b
}
