//go:build linux

package openrc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

var maintenanceID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func maintenanceGroup(id string) (string, error) {
	if !maintenanceID.MatchString(id) {
		return "", errors.New("invalid maintenance identity")
	}
	// Outside /ctlvps.slice: uninstall must be able to drain that entire tree.
	return "/ctlvps-maintenance/" + id, nil
}

func PrepareMaintenance(id string) error     { return prepareMaintenance(id, false) }
func PrepareMaintenanceHost(id string) error { return prepareMaintenance(id, true) }
func prepareMaintenance(id string, host bool) error {
	group, err := maintenanceGroup(id)
	if err != nil {
		return err
	}
	if host {
		group += "-host"
	}
	parent := "/sys/fs/cgroup"
	for _, part := range strings.Split(strings.TrimPrefix(group, "/"), "/") {
		if err = controller(parent); err != nil {
			return err
		}
		parent = filepath.Join(parent, part)
		if err = os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		if err = trustedDirectory(parent, false); err != nil {
			return err
		}
	}
	values := [][2]string{{"memory.max", "201326592"}, {"memory.high", "167772160"}, {"pids.max", "128"}}
	if host {
		values = [][2]string{{"memory.max", "67108864"}, {"memory.high", "50331648"}, {"pids.max", "64"}}
	}
	for _, kv := range values {
		if err = setLimit(parent, kv[0], kv[1]); err != nil {
			return err
		}
	}
	if err = join(group); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}

func MaintenanceActive(id string) bool {
	group, err := maintenanceGroup(id)
	if err != nil {
		return false
	}
	for _, item := range []struct{ suffix, command string }{{"-host", "maintenance-openrc-host"}, {"", "maintenance-openrc-worker"}} {
		current := group + item.suffix
		raw, err := os.ReadFile(filepath.Join(cgpath(current), "cgroup.procs"))
		if err != nil {
			continue
		}
		want := "/var/lib/ctlvps-maintenance/" + id + "/worker"
		for _, field := range strings.Fields(string(raw)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 || !inGroup(pid, current) {
				continue
			}
			exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
			if err != nil || exe != want {
				continue
			}
			cmd, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
			if err == nil && string(cmd) == strings.Join([]string{want, item.command, id}, "\x00")+"\x00" {
				return true
			}
		}
	}
	return false
}

func KillMaintenance(id string) error {
	group, err := maintenanceGroup(id)
	if err != nil {
		return err
	}
	path := cgpath(group)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	if err := os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0600); err != nil {
		return err
	}
	return waitEmpty(path)
}

// Keep the agent's OpenRC supervisor out of a transient maintenance cgroup.
func StartAgent() error {
	if err := CheckEnvironment(context.Background()); err != nil {
		return err
	}
	if err := Verify("ctlvps-agent.service"); err != nil {
		return err
	}
	group := groupRoot + "/ctlvps-agent-supervisor.service"
	if err := limits(group, 64, 128); err != nil {
		return err
	}
	if err := join(group); err != nil {
		return err
	}
	_, err := invoke(context.Background(), "/sbin/rc-service", "ctlvps-agent", "start")
	return err
}
