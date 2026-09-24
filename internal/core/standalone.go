package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func IsStandalone(kind string) bool { return kind == "snell" || kind == "mieru" }
func StandaloneUnit(kind string, port int) string {
	if kind == "mieru" {
		return MitaUnit(port)
	}
	return SnellUnit(port)
}

// Mita can keep its management loop alive after a listener error. Verify a
// socket held by the actual service process, rather than process health alone.
func (s *Systemd) CheckNodeListener(ctx context.Context, unit string, port int, network string) error {
	raw, err := s.ctl(ctx, "show", unit, "--property=MainPID", "--value")
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || pid < 1 {
		return fmt.Errorf("missing proxy process")
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return err
	}
	owned := map[string]bool{}
	for _, fd := range fds {
		target, e := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), fd.Name()))
		if e == nil && strings.HasPrefix(target, "socket:[") {
			owned[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	if network != "tcp" && network != "udp" {
		return fmt.Errorf("invalid listener transport")
	}
	for _, suffix := range []string{network, network + "6"} {
		b, e := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", pid, suffix))
		if e != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 10 {
				continue
			}
			_, hexPort, ok := strings.Cut(f[1], ":")
			p, e := strconv.ParseUint(hexPort, 16, 16)
			if ok && e == nil && int(p) == port && owned[f[9]] && (network == "udp" || f[3] == "0A") {
				return nil
			}
		}
	}
	return fmt.Errorf("proxy process has not opened its configured listener")
}
