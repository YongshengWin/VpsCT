package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/mieruconfig"
	"encoding/json"
)

// Mita drives the official mita, one process per node (template
// unit ctlvps-mita@<port>.service) so nodes can be started/stopped and
// restarted independently.
type Mita struct {
	Paths   Paths
	Systemd *Systemd
}

// NewMita builds the driver.
func NewMita(p Paths, sd *Systemd) *Mita { return &Mita{Paths: p, Systemd: sd} }

// Name implements Driver.
func (d *Mita) Name() string { return "mieru" }

func (d *Mita) bin() string { return filepath.Join(d.Paths.BinDir, "mita") }

func (d *Mita) confDir() string { return filepath.Join(d.Paths.ConfDir, "mita") }

func mitaUnitFor(port int) string { return MitaUnit(port) }

// MitaUnit is the systemd instance for a listen port.
func MitaUnit(port int) string { return fmt.Sprintf("ctlvps-mita@%d.service", port) }

// EnsureInstalled implements Driver.
func (d *Mita) EnsureInstalled(ctx context.Context, v agentproto.CoreVersion) (bool, error) {
	if recordedVersion(d.bin()) == v.Version && v.Version != "" {
		if _, err := os.Stat(d.bin()); err == nil {
			return false, nil
		}
	}
	if err := installBinary(ctx, d.Paths.BinDir, "mita", v); err != nil {
		return false, fmt.Errorf("install mita %s: %w", v.Version, err)
	}
	return true, nil
}

// Config renders mita.conf for one node.
func MitaConfig(n agentproto.NodeSpec, ipv4Only bool) string {
	cfg := map[string]any{"portBindings": []any{map[string]any{"port": n.ListenPort, "protocol": str(n.Params, "transport")}}, "users": []any{map[string]any{"name": str(n.Params, "username"), "password": str(n.Params, "password"), "allowPrivateIP": n.AllowPrivate, "allowLoopbackIP": false}}, "loggingLevel": "ERROR", "mtu": 1400}
	if n.RuntimeNetwork != nil {
		cfg["listenIPAddress"] = n.RuntimeNetwork.ListenAddress
	}
	policy := "PREFER_IPv4"
	if ipv4Only {
		policy = "ONLY_IPv4"
	}
	cfg["dns"] = map[string]any{"dualStack": policy}
	raw, _ := json.Marshal(cfg)
	return string(raw)
}

// Apply implements Driver.
func (d *Mita) Apply(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (bool, error) {
	for _, n := range nodes {
		if err := agentproto.ValidateParams(n.Params, 0); err != nil {
			return false, err
		}
		if _, err := mieruconfig.Decode(n.Params); err != nil {
			return false, err
		}
		if err := validateNodeNetwork(n, ds); err != nil {
			return false, err
		}
		if n.Core != "mieru" || n.Protocol != "mieru" {
			return false, errors.New("mieru 协议与内核不一致")
		}
		if n.ListenPort < 1025 || n.ListenPort > 65535 {
			return false, errors.New("invalid listen port")
		}
	}
	changed := false
	restartBinary := activationPending(d.bin())
	launcher, err := proxyLauncher()
	if err != nil {
		return false, err
	}
	if err = d.Systemd.EnsureProxyGuard(ctx, launcher); err != nil {
		return false, err
	}
	want := map[int]bool{}
	sorted := append([]agentproto.NodeSpec(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ListenPort < sorted[j].ListenPort })
	for _, n := range sorted {
		if n.Blocked {
			continue
		}
		want[n.ListenPort] = true
		meterChanged, err := d.Systemd.EnsureSnellMeter(ctx, n)
		if err != nil {
			return changed, err
		}
		user := "ctlvps-mi" + strconv.Itoa(n.ListenPort)
		_, gid, err := proxyIdentity(user)
		if err != nil {
			return changed, err
		}
		dir := filepath.Join(proxyConfigRoot, "mita-"+strconv.Itoa(n.ListenPort))
		if err = secureDir(dir, 0, int(gid), 0750); err != nil {
			return changed, err
		}
		path := filepath.Join(dir, "config.json")
		oldConfig, configErr := os.ReadFile(path)
		u := mitaUnitFor(n.ListenPort)
		unitPath := filepath.Join(d.Systemd.UnitDir, u)
		oldUnit, unitErr := os.ReadFile(unitPath)
		wasActive := d.Systemd.IsActive(ctx, u)
		enabledBefore, _ := d.Systemd.ctl(ctx, "is-enabled", u)
		props := proxyProperties(user, dir, "-/var/log/ctlvps/mita-"+strconv.Itoa(n.ListenPort)+".log", SnellSlice(n.NodeID), "", "", false)
		unit := ServiceUnit("ctlvps mita", launcher+" proxy-exec mieru run "+strconv.Itoa(n.ListenPort), ds.Tuning, append(props, "IPAccounting=yes")...)
		unitChanged, err := d.Systemd.WriteUnit(u, unit)
		if err != nil {
			return changed, err
		}
		rollback := func(cause error) error {
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			errs := []error{cause, d.Systemd.StopUnits(c, []string{u})}
			if strings.TrimSpace(enabledBefore) != "enabled" {
				_, e := d.Systemd.ctl(c, "disable", u)
				errs = append(errs, e)
			}
			if configErr == nil {
				_, e := proxyFile(path, oldConfig, int(gid))
				errs = append(errs, e)
			} else {
				_ = os.Remove(path)
			}
			if unitErr == nil {
				_, e := WriteIfChanged(unitPath, oldUnit, 0644)
				errs = append(errs, e)
			} else {
				_ = os.Remove(unitPath)
			}
			errs = append(errs, d.Systemd.DaemonReload(c))
			if wasActive {
				errs = append(errs, d.Systemd.StartUnits(c, []string{u}))
			}
			return errors.Join(errs...)
		}
		c, err := proxyFile(path, []byte(MitaConfig(n, ds.IPv4Only)), int(gid))
		if err != nil {
			return changed, rollback(err)
		}
		if c || unitChanged || meterChanged || restartBinary || !d.Systemd.IsActive(ctx, u) {
			if err := d.Systemd.DaemonReload(ctx); err != nil {
				return changed, rollback(err)
			}
			if err := d.Systemd.EnableRestart(ctx, u); err != nil {
				return changed, rollback(err)
			}
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return changed, rollback(ctx.Err())
			case <-timer.C:
			}
			if !d.Systemd.IsActive(ctx, u) {
				return changed, rollback(fmt.Errorf("isolated Mita activation failed"))
			}
			if err = d.Systemd.CheckRunningProxy(ctx, u, user, SnellSlice(n.NodeID), launcher+" proxy-exec mieru run "+strconv.Itoa(n.ListenPort), false); err != nil {
				return changed, rollback(err)
			}
			if err = d.Systemd.CheckNodeListener(ctx, u, n.ListenPort, strings.ToLower(str(n.Params, "transport"))); err != nil {
				return changed, rollback(err)
			}
			changed = true
		} else if err = d.Systemd.CheckRunningProxy(ctx, u, user, SnellSlice(n.NodeID), launcher+" proxy-exec mieru run "+strconv.Itoa(n.ListenPort), false); err != nil {
			return changed, err
		}
	}
	// stop instances that are no longer wanted (blocked / removed)
	for _, u := range d.Systemd.ListUnits(ctx, "ctlvps-mita@*.service") {
		portStr := strings.TrimSuffix(strings.TrimPrefix(u, "ctlvps-mita@"), ".service")
		port, err := strconv.Atoi(portStr)
		if err != nil || want[port] {
			continue
		}
		if err := d.Systemd.StopDisable(ctx, u); err != nil {
			return changed, err
		}
		if err := os.Remove(filepath.Join(proxyConfigRoot, "mita-"+portStr, "config.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return changed, err
		}
		changed = true
	}
	// also remove orphan conf files (units never started)
	if entries, err := os.ReadDir(d.confDir()); err == nil {
		for _, e := range entries {
			p, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".conf"))
			if err == nil && !want[p] {
				_ = os.Remove(filepath.Join(d.confDir(), e.Name()))
			}
		}
	}
	if err := completeActivation(d.bin()); err != nil {
		return changed, err
	}
	return changed, nil
}

// Status implements Driver (aggregated over instances).
func (d *Mita) Status(ctx context.Context) agentproto.CoreStatus {
	st := agentproto.CoreStatus{Name: "mita", Version: recordedVersion(d.bin())}
	_, err := os.Stat(d.bin())
	st.Installed = err == nil
	units := d.Systemd.ListUnits(ctx, "ctlvps-mita@*.service")
	st.Instances = len(units)
	active := 0
	for _, u := range units {
		s := d.Systemd.Show(ctx, u)
		if s.Active {
			active++
		}
		st.RSSBytes += s.RSSBytes
		st.NRestarts += s.NRestarts
		if s.LastError != "" && st.LastError == "" {
			st.LastError = u + ": " + s.LastError
		}
		if st.Since.IsZero() || (!s.Since.IsZero() && s.Since.Before(st.Since)) {
			st.Since = s.Since
		}
	}
	st.Active = len(units) > 0 && active == len(units)
	if len(units) > 0 && active < len(units) && st.LastError == "" {
		st.LastError = fmt.Sprintf("%d/%d instances active", active, len(units))
	}
	return st
}

// Stop implements Driver.
func (d *Mita) Stop(ctx context.Context) error {
	var errs []error
	for _, u := range d.Systemd.ListUnits(ctx, "ctlvps-mita@*.service") {
		if err := d.Systemd.StopDisable(ctx, u); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
