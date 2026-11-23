package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/openrc"
)

// XrayPlatform checks the actual init, not just whether systemctl is installed.
func XrayPlatform() bool {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || openrc.Enabled() {
		return false
	}
	if _, e := os.Stat("/etc/alpine-release"); !os.IsNotExist(e) {
		return false
	}
	init, err := os.ReadFile("/proc/1/comm")
	return err == nil && strings.TrimSpace(string(init)) == "systemd"
}

type Xray struct {
	Paths   Paths
	Systemd *Systemd
}

func NewXray(p Paths, s *Systemd) *Xray { return &Xray{p, s} }
func (*Xray) Name() string              { return "xray" }
func (d *Xray) bin() string             { return filepath.Join(d.Paths.BinDir, "xray") }
func (d *Xray) EnsureInstalled(ctx context.Context, v agentproto.CoreVersion) (bool, error) {
	if !XrayPlatform() || !agentproto.OfficialXrayPin(v) {
		return false, errors.New("Xray requires the official pinned Linux AMD64 systemd release")
	}
	if pending, err := recoverCoreInstallation(ctx, d.bin(), d.Systemd); err != nil || pending {
		return false, err
	}
	if recordedVersion(d.bin()) == v.Version {
		return false, nil
	}
	if err := discardStagedCore(ctx, d.bin(), d.Systemd); err != nil {
		return false, err
	}
	if err := installBinary(ctx, d.Paths.BinDir, "xray", v); err != nil {
		return false, err
	}
	return true, nil
}

// XrayConfig renders only the closed XHTTP/REALITY profile. Credentials never
// enter argv, unit files, logs, diagnostics or receipts.
func XrayConfig(ds *agentproto.DesiredState, n agentproto.NodeSpec) ([]byte, error) {
	if err := agentproto.ValidateXrayNode(ds, n); err != nil {
		return nil, err
	}
	p := n.Params
	x := map[string]any{"path": p["xhttp_path"], "mode": p["xhttp_mode"]}
	if h, _ := p["xhttp_host"].(string); h != "" {
		x["host"] = h
	}
	host := str(p, "handshake_server")
	listen := "::"
	strategy := "UseIP"
	if ds.IPv4Only {
		listen = "0.0.0.0"
		strategy = "ForceIPv4"
	}
	inbound := map[string]any{"listen": listen, "port": n.ListenPort, "protocol": "vless", "settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": p["uuid"]}}}, "streamSettings": map[string]any{"network": "xhttp", "security": "reality", "xhttpSettings": x, "realitySettings": map[string]any{"target": host + ":443", "serverNames": []string{host}, "privateKey": p["reality_private_key"], "shortIds": []any{p["reality_short_id"]}}}}
	return json.Marshal(map[string]any{"log": map[string]string{"loglevel": "none"}, "inbounds": []any{inbound}, "outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]string{"domainStrategy": strategy}}}})
}

func (d *Xray) checkCandidate(ctx context.Context, launcher, port string, props []string) error {
	name := "ctlvps-proxy-check-xray-" + port + ".service"
	props = append(append([]string{}, props...), "Type=oneshot", "Restart=no", "Slice=system.slice", "PrivateNetwork=true", "CapabilityBoundingSet=", "AmbientCapabilities=", "StandardOutput=null", "StandardError=null", "TimeoutStartSec=20s")
	if _, err := d.Systemd.WriteUnit(name, ServiceUnit("VpsCT isolated Xray configuration check", launcher+" proxy-exec xray candidate "+port, agentproto.Tuning{}, props...)); err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.Systemd.StopUnits(c, []string{name})
		_ = os.Remove(filepath.Join(d.Systemd.UnitDir, name))
		_ = d.Systemd.DaemonReload(c)
	}()
	if err := d.Systemd.DaemonReload(ctx); err != nil {
		return err
	}
	if err := d.Systemd.StartUnits(ctx, []string{name}); err != nil {
		return errors.New("isolated Xray candidate check failed")
	}
	return nil
}

type xrayInstance struct {
	node                     agentproto.NodeSpec
	unit, dir, user, command string
	config                   []byte
	gid                      int
	properties               []string
}

func (d *Xray) Apply(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (changed bool, applyErr error) {
	if !XrayPlatform() {
		return false, errors.New("Xray requires Linux AMD64 systemd")
	}
	if ds == nil {
		return false, errors.New("missing Xray desired state")
	}
	if len(nodes) == 0 && ds.XrayVersion == 0 {
		return false, nil
	} // no side effects for pre-extension hosts
	policy := corePolicyFingerprint(nodes, nil)
	defer func() {
		if applyErr != nil {
			applyErr = rollbackCoreAfterFailure(d.bin(), d.Systemd, policy, applyErr)
		}
	}()
	units, configs := standaloneRecoveryPaths(ctx, d.bin(), d.Systemd, nodes)
	if err := beginCoreApply(ctx, d.bin(), d.Systemd, policy, units, configs); err != nil {
		return false, err
	}
	if err := setCoreRetainedIntent(d.bin(), ds, nodes, nil); err != nil {
		return false, err
	}
	launcher, err := proxyLauncher()
	if err != nil {
		return false, err
	}
	if err = d.Systemd.EnsureProxyGuard(ctx, launcher); err != nil {
		return false, err
	}
	live := map[string]bool{}
	seen := map[int]bool{}
	instances := []xrayInstance{}
	for _, n := range nodes {
		if err := agentproto.ValidateXrayNode(ds, n); err != nil {
			return false, err
		}
		if seen[n.ListenPort] {
			return false, errors.New("duplicate Xray port")
		}
		seen[n.ListenPort] = true
		if n.Blocked || n.Retired {
			continue
		}
		config, err := XrayConfig(ds, n)
		if err != nil {
			return false, err
		}
		port := strconv.Itoa(n.ListenPort)
		user := "ctlvps-xr" + port
		_, gid, err := proxyIdentity(user)
		if err != nil {
			return false, err
		}
		dir := filepath.Join(proxyConfigRoot, "xray-"+port)
		if err := secureDir(dir, 0, int(gid), 0750); err != nil {
			return false, err
		}
		props := proxyProperties(user, dir, "", SnellSlice(n.NodeID), "", "", false)
		props = append(props, "StandardOutput=null", "StandardError=null")
		candidate := filepath.Join(dir, "candidate.json")
		if _, err := proxyFile(candidate, config, int(gid)); err != nil {
			return false, err
		}
		err = d.checkCandidate(ctx, launcher, port, props)
		removeErr := os.Remove(candidate)
		if err = errors.Join(err, removeErr); err != nil {
			return false, err
		}
		unit := StandaloneUnit("xray", n.ListenPort)
		live[unit] = true
		instances = append(instances, xrayInstance{n, unit, dir, user, launcher + " proxy-exec xray run " + port, config, int(gid), props})
	}
	// All candidate files passed the official binary under isolation before any
	// running unit's configuration is touched. A restart uses the durable journal.
	if err := markCoreApplying(d.bin()); err != nil {
		return false, err
	}
	for _, unit := range units {
		if live[unit] {
			continue
		}
		if _, err := os.Stat(filepath.Join(d.Systemd.UnitDir, unit)); os.IsNotExist(err) && !d.Systemd.IsActive(ctx, unit) {
			continue
		}
		if err := d.Systemd.StopDisable(ctx, unit); err != nil {
			return changed, err
		}
		_, port, _ := strings.Cut(unit, "@")
		port = strings.TrimSuffix(port, ".service")
		for _, path := range []string{filepath.Join(proxyConfigRoot, "xray-"+port, "config.json"), filepath.Join(proxyConfigRoot, "xray-"+port, "candidate.json"), filepath.Join(d.Systemd.UnitDir, unit)} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return changed, err
			}
		}
		changed = true
	}
	for _, i := range instances {
		ch, err := d.Systemd.WriteUnit(i.unit, ServiceUnit("VpsCT Xray", i.command, ds.Tuning, i.properties...))
		if err != nil {
			return changed, err
		}
		c, err := proxyFile(filepath.Join(i.dir, "config.json"), i.config, i.gid)
		if err != nil {
			return changed, err
		}
		if ch || c || activationPending(d.bin()) || !d.Systemd.IsActive(ctx, i.unit) {
			if err := d.Systemd.DaemonReload(ctx); err != nil {
				return changed, err
			}
			if err := d.Systemd.EnableRestart(ctx, i.unit); err != nil {
				return changed, err
			}
			changed = true
		}
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		for {
			err = d.Systemd.CheckNodeListener(check, i.unit, i.node.ListenPort, "tcp")
			if err == nil {
				break
			}
			select {
			case <-check.Done():
				cancel()
				return changed, errors.New("Xray process did not acquire its TCP listener")
			case <-time.After(100 * time.Millisecond):
			}
		}
		cancel()
		if err := d.Systemd.CheckRunningProxy(ctx, i.unit, i.user, SnellSlice(i.node.NodeID), i.command, false); err != nil {
			return changed, err
		}
		if !d.runningCurrent(ctx, i.unit) {
			return changed, errors.New("Xray process does not run the installed binary")
		}
	}
	if changed {
		if err := d.Systemd.DaemonReload(ctx); err != nil {
			return changed, err
		}
	}
	return changed, markCoreValidated(d.bin())
}
func (d *Xray) runningCurrent(ctx context.Context, unit string) bool {
	pid, err := d.Systemd.ctl(ctx, "show", unit, "--property=MainPID", "--value")
	i, err2 := strconv.Atoi(strings.TrimSpace(pid))
	if err != nil || err2 != nil || i < 1 {
		return false
	}
	process, err := os.Stat(fmt.Sprintf("/proc/%d/exe", i))
	bin, e := os.Stat(d.bin())
	return err == nil && e == nil && os.SameFile(process, bin)
}
func (d *Xray) FinishActivation(ctx context.Context, success bool) error {
	return finishCoreActivation(ctx, d.bin(), d.Systemd, success)
}
func (d *Xray) RetainedReady(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, _ []agentproto.ForwardSpec) bool {
	return retainedStandalone(ctx, d.bin(), d.Systemd, ds, nodes)
}
func (d *Xray) Stop(ctx context.Context) error {
	var errs []error
	for _, u := range managedCoreUnits(ctx, d.bin(), d.Systemd, nil) {
		errs = append(errs, d.Systemd.StopDisable(ctx, u))
	}
	return errors.Join(errs...)
}
func (d *Xray) Status(ctx context.Context) agentproto.CoreStatus {
	v := recordedVersion(d.bin())
	s := agentproto.CoreStatus{Name: "xray", Version: v, Installed: v != ""}
	for _, u := range d.Systemd.ListUnits(ctx, "ctlvps-xray@*.service") {
		if !coreUnitAllowed(d.bin(), u) {
			continue
		}
		p := d.Systemd.Show(ctx, u)
		s.Instances++
		s.RSSBytes += p.RSSBytes
		s.NRestarts += p.NRestarts
		if !p.Active {
			s.LastError = "Xray instance is not active"
		}
	}
	s.Active = s.Instances > 0 && s.LastError == ""
	return s
}

// An unrelated conflicting new listener must not make an old, working Xray
// endpoint disappear. After a complete rollback only, re-render current authority
// and compare to the restored bytes. Re-admit the exact matches through the full
// sandbox, listener, binary, ingress and receipt gates; never copy old credentials.
func (d *Xray) RestoreUnchanged(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (map[int64]bool, error) {
	if journal, err := loadCoreRecovery(d.bin()); err != nil || journal != nil {
		return nil, errors.New("Xray rollback is incomplete")
	}
	if recordedVersion(d.bin()) != agentproto.PinnedXray().Version {
		return nil, nil
	}
	if _, err := os.Stat(d.bin() + ".retained"); err != nil {
		return nil, nil
	}
	keep := []agentproto.NodeSpec{}
	ids := map[int64]bool{}
	for _, n := range nodes {
		if n.Blocked || n.Retired {
			continue
		}
		render, err := XrayConfig(ds, n)
		if err != nil {
			continue
		}
		path := filepath.Join(proxyConfigRoot, "xray-"+strconv.Itoa(n.ListenPort), "config.json")
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() != int64(len(render)) {
			continue
		}
		// A port can be reused by a new node; identical rendered bytes alone
		// are not a stable resource identity. Require the old node-ID slice.
		unit, err := os.ReadFile(filepath.Join(d.Systemd.UnitDir, StandaloneUnit("xray", n.ListenPort)))
		if err != nil || !bytes.Contains(unit, []byte("\nSlice="+SnellSlice(n.NodeID)+"\n")) {
			continue
		}
		old, err := os.ReadFile(path)
		if err == nil && bytes.Equal(old, render) {
			keep = append(keep, n)
			ids[n.NodeID] = true
		}
	}
	if len(keep) == 0 || len(keep) == len(nodes) {
		return nil, nil
	}
	_, err := d.Apply(ctx, ds, keep)
	if err != nil {
		return nil, err
	}
	return ids, nil
}
