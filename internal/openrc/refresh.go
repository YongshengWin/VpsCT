package openrc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"ctlvps/internal/secureupdate"
)

const refreshFile = "/run/ctlvps-openrc/refresh.json"

type helperRefresh struct {
	Version int      `json:"version"`
	Units   []string `json:"units"`
}

func decodeRefresh(b []byte) (helperRefresh, error) {
	var p helperRefresh
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if p.Version != 1 || len(p.Units) > 4096 {
		return p, errors.New("invalid OpenRC helper refresh")
	}
	seen := map[string]bool{}
	for _, unit := range p.Units {
		_, mita := mitaPort(unit)
		if (unit != "ctlvps-singbox.service" && unit != "ctlvps-singbox-private.service" && !mita) || seen[unit] {
			return p, errors.New("invalid OpenRC helper restart identity")
		}
		seen[unit] = true
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, errors.New("trailing OpenRC helper refresh data")
	}
	return p, nil
}

// RefreshHelpers is called under the agent configuration lock on every start.
// Atomic launcher replacement leaves long-lived helpers on the old inode. The
// service-host also owns supervisor children, so stop and remember live proxies
// before replacing it. A protected receipt resumes an interrupted refresh even
// without the controller; paused/stopped proxies are never added to this set.
func RefreshHelpers(ctx context.Context) error {
	if !Enabled() {
		return nil
	}
	p := helperRefresh{Version: 1}
	resume := false
	if b, err := secureupdate.ReadProtectedConfig(refreshFile); err == nil {
		var err error
		p, err = decodeRefresh(b)
		if err != nil {
			return err
		}
		resume = true
	} else if !os.IsNotExist(err) {
		return err
	}
	stale := false
	for _, unit := range []string{"ctlvps-service-host.service", "ctlvps-proxy-guard.service"} {
		current, err := helperCurrent(unit)
		if err != nil {
			return err
		}
		stale = stale || !current
	}
	if !resume && !stale {
		// A deliberately stopped proxy stays stopped, but an installed network
		// guard must be restored before the agent can declare startup readiness.
		if _, err := os.Lstat(filepath.Join(UnitDir, "ctlvps-proxy-guard.service")); err == nil {
			return EnsureGuard(ctx, launcher)
		} else if !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// Regenerate fixed scripts with the new executable before executing any of
	// them. Do not ask the old service-host to interpret a new script definition.
	if err := Sync(); err != nil {
		return err
	}
	entries, err := os.ReadDir(UnitDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	proxies := []string{"ctlvps-singbox.service", "ctlvps-singbox-private.service"}
	for _, entry := range entries {
		if _, ok := mitaPort(entry.Name()); ok {
			proxies = append(proxies, entry.Name())
		}
	}
	if !resume {
		for _, unit := range proxies {
			if populated(unit) {
				if err := Verify(unit); err != nil {
					return err
				}
				p.Units = append(p.Units, unit)
			}
		}
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err = writeProtected(refreshFile, b, 0600); err != nil {
			return err
		}
	}
	// Disable watchdog recovery while draining its service-host descendants.
	for _, unit := range append(append([]string{"ctlvps-proxy-guard.service"}, proxies...), "ctlvps-service-host.service") {
		if _, err := controlLocal(ctx, "stop", unit); err != nil {
			return fmt.Errorf("refresh OpenRC helper stop %s: %w", unit, err)
		}
	}
	if err := EnsureGuard(ctx, launcher); err != nil {
		return err
	}
	// Repair/verify firewall readiness synchronously before restarting business
	// processes. The independently running watchdog uses the same policy lock.
	if _, err := invoke(ctx, launcher, "proxy-guard"); err != nil {
		return err
	}
	for _, unit := range p.Units {
		if _, err := Control(ctx, "start", unit); err != nil {
			return err
		}
	}
	for _, unit := range append([]string{"ctlvps-service-host.service", "ctlvps-proxy-guard.service"}, p.Units...) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			pid, err := MainPID(unit)
			if err != nil {
				return err
			}
			if pid > 0 {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("refreshed OpenRC service not running: %s", unit)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return os.Remove(refreshFile)
}
