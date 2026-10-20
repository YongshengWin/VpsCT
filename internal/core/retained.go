package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

// A retained service must have exactly the already admitted endpoints and
// runtime configuration. Unlike the authorization fence used for rollback,
// this includes port, cipher and locally resolved network observations.
func coreRetainedFingerprint(ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) string {
	ns := append([]agentproto.NodeSpec(nil), nodes...)
	fs := append([]agentproto.ForwardSpec(nil), forwards...)
	sort.Slice(ns, func(i, j int) bool { return ns[i].NodeID < ns[j].NodeID })
	sort.Slice(fs, func(i, j int) bool { return fs[i].ForwardID < fs[j].ForwardID })
	state := []any{ds.PublicHost, ds.IPv4Only, corePolicyFingerprint(ns, fs)}
	for _, n := range ns {
		n.Name = ""
		state = append(state, []any{n, retainedRuntime(n.RuntimeNetwork)})
	}
	for _, f := range fs {
		state = append(state, []any{f, retainedRuntime(f.RuntimeNetwork)})
	}
	b, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func retainedRuntime(r *networkconfig.Resolved) *networkconfig.Resolved {
	if r == nil {
		return nil
	}
	clone := *r
	clone.Sequence = 0
	if r.SOCKS5 != nil {
		transport := r.SOCKS5.WithoutDNSLifetime()
		clone.SOCKS5 = &transport
	}
	if r.ForwardTarget != nil {
		target := r.ForwardTarget.WithoutDNSLifetime()
		clone.ForwardTarget = &target
	}
	return &clone
}
func retainedRuntimeFresh(r *networkconfig.Resolved) bool {
	if r == nil {
		return true
	}
	return (r.SOCKS5 == nil || !r.SOCKS5.DNSExpired(time.Now())) && (r.ForwardTarget == nil || !r.ForwardTarget.DNSExpired(time.Now()))
}
func setCoreRetainedIntent(binary string, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) error {
	r, err := loadCoreRecovery(binary)
	if err != nil || r == nil {
		return err
	}
	r.Retained = coreRetainedFingerprint(ds, nodes, forwards)
	return writeCoreRecovery(binary, r)
}
func retainedCoreIdentity(ctx context.Context, binary string, sd *Systemd, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) (string, bool) {
	for _, n := range nodes {
		if !retainedRuntimeFresh(n.RuntimeNetwork) {
			return "", false
		}
	}
	for _, f := range forwards {
		if !retainedRuntimeFresh(f.RuntimeNetwork) {
			return "", false
		}
	}
	policy, err := os.ReadFile(binary + ".policy")
	if err != nil || strings.TrimSpace(string(policy)) != corePolicyFingerprint(nodes, forwards) {
		return "", false
	}
	intent, err := os.ReadFile(binary + ".retained")
	want := coreRetainedFingerprint(ds, nodes, forwards)
	if err != nil || want == "" || strings.TrimSpace(string(intent)) != want {
		return "", false
	}
	r, err := loadCoreRecovery(binary)
	if err != nil {
		return "", false
	}
	if r != nil {
		if r.Phase != "installing" && r.Phase != "installed" {
			return "", false
		}
		if validateCoreRecovery(binary, sd, r) != nil {
			return "", false
		}
		for _, f := range r.Files {
			if f.Path == binary && f.Exists {
				return f.SHA256, true
			}
		}
		return "", false
	}
	f, err := os.Open(binary)
	if err != nil {
		return "", false
	}
	defer f.Close()
	digest, err := FileDigest(f)
	return digest, err == nil
}
func retainedProcess(ctx context.Context, sd *Systemd, unit, digest string) bool {
	if !sd.IsActive(ctx, unit) {
		return false
	}
	pid, err := sd.ctl(ctx, "show", unit, "--property=MainPID", "--value")
	value, e := strconv.Atoi(strings.TrimSpace(pid))
	if err != nil || e != nil || value < 1 {
		return false
	}
	f, err := os.Open(filepath.Join("/proc", strconv.Itoa(value), "exe"))
	if err != nil {
		return false
	}
	defer f.Close()
	got, err := FileDigest(f)
	return err == nil && got == digest
}
func (d *SingBox) RetainedReady(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) bool {
	digest, ok := retainedCoreIdentity(ctx, d.bin(), d.Systemd, ds, nodes, forwards)
	if !ok {
		return false
	}
	launcher, err := proxyLauncher()
	if err != nil {
		return false
	}
	wantPublic, wantPrivate := false, false
	for _, n := range nodes {
		if !n.Blocked && !n.Retired {
			if n.AllowPrivate {
				wantPrivate = true
			} else {
				wantPublic = true
			}
		}
	}
	for _, f := range forwards {
		wantPublic = wantPublic || (!f.Blocked && !f.Retired)
	}
	for _, private := range []bool{false, true} {
		if (!private && !wantPublic) || (private && !wantPrivate) {
			continue
		}
		unit, account, profile := singboxUnit, "ctlvps-sb", "public"
		if private {
			unit, account, profile = "ctlvps-singbox-private.service", "ctlvps-sp", "private"
		}
		if !retainedProcess(ctx, d.Systemd, unit, digest) || d.Systemd.CheckRunningProxy(ctx, unit, account, SingBoxSlice(private), launcher+" proxy-exec singbox run "+profile, true) != nil {
			return false
		}
	}
	return wantPublic || wantPrivate
}
func retainedStandalone(ctx context.Context, binary string, sd *Systemd, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) bool {
	digest, ok := retainedCoreIdentity(ctx, binary, sd, ds, nodes, nil)
	if !ok {
		return false
	}
	launcher, err := proxyLauncher()
	if err != nil {
		return false
	}
	kind, accountPrefix := "snell", "ctlvps-sn"
	if filepath.Base(binary) == "mita" {
		kind, accountPrefix = "mieru", "ctlvps-mi"
	}
	live := false
	for _, n := range nodes {
		if n.Blocked || n.Retired {
			continue
		}
		live = true
		unit := StandaloneUnit(kind, n.ListenPort)
		port := strconv.Itoa(n.ListenPort)
		if !retainedProcess(ctx, sd, unit, digest) || sd.CheckRunningProxy(ctx, unit, accountPrefix+port, SnellSlice(n.NodeID), launcher+" proxy-exec "+kind+" run "+port, false) != nil {
			return false
		}
		if kind == "mieru" && sd.CheckNodeListener(ctx, unit, n.ListenPort, strings.ToLower(str(n.Params, "transport"))) != nil {
			return false
		}
	}
	return live
}
func (d *Snell) RetainedReady(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, _ []agentproto.ForwardSpec) bool {
	return retainedStandalone(ctx, d.bin(), d.Systemd, ds, nodes)
}
func (d *Mita) RetainedReady(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, _ []agentproto.ForwardSpec) bool {
	return retainedStandalone(ctx, d.bin(), d.Systemd, ds, nodes)
}
