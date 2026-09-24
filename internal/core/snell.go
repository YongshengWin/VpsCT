package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
)

// Snell drives the official snell-server, one process per node (template
// unit ctlvps-snell@<port>.service) so nodes can be started/stopped and
// restarted independently.
type Snell struct {
	Paths   Paths
	Systemd *Systemd
}

// NewSnell builds the driver.
func NewSnell(p Paths, sd *Systemd) *Snell { return &Snell{Paths: p, Systemd: sd} }

const snellTemplateUnit = "ctlvps-snell@.service"

// Name implements Driver.
func (d *Snell) Name() string { return "snell" }

func (d *Snell) bin() string { return filepath.Join(d.Paths.BinDir, "snell-server") }

func (d *Snell) confDir() string { return filepath.Join(d.Paths.ConfDir, "snell") }

func unitFor(port int) string { return SnellUnit(port) }

// SnellUnit is the systemd instance for a listen port.
func SnellUnit(port int) string { return fmt.Sprintf("ctlvps-snell@%d.service", port) }

// EnsureInstalled implements Driver.
func (d *Snell) EnsureInstalled(ctx context.Context, v agentproto.CoreVersion) (bool, error) {
	if recordedVersion(d.bin()) == v.Version && v.Version != "" {
		if _, err := os.Stat(d.bin()); err == nil {
			return false, nil
		}
	}
	if err := installBinary(ctx, d.Paths.BinDir, "snell-server", v); err != nil {
		return false, fmt.Errorf("install snell-server %s: %w", v.Version, err)
	}
	return true, nil
}

// Config renders snell-server.conf for one node.
func Config(n agentproto.NodeSpec, ipv4Only bool) string {
	var b strings.Builder
	b.WriteString("[snell-server]\n")
	listen := fmt.Sprintf(":::%d", n.ListenPort)
	if n.RuntimeNetwork != nil {
		listen = net.JoinHostPort(n.RuntimeNetwork.ListenAddress, strconv.Itoa(n.ListenPort))
	}
	fmt.Fprintf(&b, "listen = %s\n", listen)
	fmt.Fprintf(&b, "psk = %s\n", str(n.Params, "psk"))
	obfs := str(n.Params, "obfs")
	if obfs == "" {
		obfs = "off"
	}
	fmt.Fprintf(&b, "obfs = %s\n", obfs)
	if ipv4Only {
		b.WriteString("ipv6 = false\n")
	} else {
		b.WriteString("ipv6 = true\n")
	}
	if v := str(n.Params, "version"); v == "5" {
		b.WriteString("version = 5\n")
	}
	return b.String()
}

// Apply implements Driver.
func (d *Snell) Apply(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (bool, error) {
	for _, n := range nodes {
		if err := validateNodeNetwork(n, ds); err != nil {
			return false, err
		}
		if err := agentproto.ValidateParams(n.Params, 0); err != nil {
			return false, err
		}
		if n.ListenPort < 1 || n.ListenPort > 65535 {
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
		user := "ctlvps-sn" + strconv.Itoa(n.ListenPort)
		_, gid, err := proxyIdentity(user)
		if err != nil {
			return changed, err
		}
		dir := filepath.Join(proxyConfigRoot, snellProfile(n.ListenPort))
		if err = secureDir(dir, 0, int(gid), 0750); err != nil {
			return changed, err
		}
		path := filepath.Join(dir, "config.conf")
		oldConfig, configErr := os.ReadFile(path)
		u := unitFor(n.ListenPort)
		unitPath := filepath.Join(d.Systemd.UnitDir, u)
		oldUnit, unitErr := os.ReadFile(unitPath)
		wasActive := d.Systemd.IsActive(ctx, u)
		enabledBefore, _ := d.Systemd.ctl(ctx, "is-enabled", u)
		props := proxyProperties(user, dir, "-/var/log/ctlvps/snell-"+strconv.Itoa(n.ListenPort)+".log", SnellSlice(n.NodeID), "", "", false)
		unit := ServiceUnit("ctlvps snell-server", launcher+" proxy-exec snell run "+strconv.Itoa(n.ListenPort), ds.Tuning, append(props, "IPAccounting=yes")...)
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
		c, err := proxyFile(path, []byte(Config(n, ds.IPv4Only)), int(gid))
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
				return changed, rollback(fmt.Errorf("isolated Snell activation failed"))
			}
			if err = d.Systemd.CheckRunningProxy(ctx, u, user, SnellSlice(n.NodeID), launcher+" proxy-exec snell run "+strconv.Itoa(n.ListenPort), false); err != nil {
				return changed, rollback(err)
			}
			changed = true
		} else if err = d.Systemd.CheckRunningProxy(ctx, u, user, SnellSlice(n.NodeID), launcher+" proxy-exec snell run "+strconv.Itoa(n.ListenPort), false); err != nil {
			return changed, err
		}
	}
	// stop instances that are no longer wanted (blocked / removed)
	for _, u := range d.Systemd.ListUnits(ctx, "ctlvps-snell@*.service") {
		portStr := strings.TrimSuffix(strings.TrimPrefix(u, "ctlvps-snell@"), ".service")
		port, err := strconv.Atoi(portStr)
		if err != nil || want[port] {
			continue
		}
		if err := d.Systemd.StopDisable(ctx, u); err != nil {
			return changed, err
		}
		_ = os.Remove(filepath.Join(d.confDir(), portStr+".conf"))
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
func (d *Snell) Status(ctx context.Context) agentproto.CoreStatus {
	st := agentproto.CoreStatus{Name: "snell-server", Version: recordedVersion(d.bin())}
	_, err := os.Stat(d.bin())
	st.Installed = err == nil
	units := d.Systemd.ListUnits(ctx, "ctlvps-snell@*.service")
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
func (d *Snell) Stop(ctx context.Context) error {
	var errs []error
	for _, u := range d.Systemd.ListUnits(ctx, "ctlvps-snell@*.service") {
		if err := d.Systemd.StopDisable(ctx, u); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
