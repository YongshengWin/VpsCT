package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
)

// Systemd wraps systemctl.
type Systemd struct {
	UnitDir string // /etc/systemd/system
}

// NewSystemd returns the default wrapper.
func NewSystemd() *Systemd { return &Systemd{UnitDir: "/etc/systemd/system"} }

// Available reports whether systemctl works.
func (s *Systemd) Available(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "--version").Run() == nil
}

func (s *Systemd) ctl(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// WriteUnit writes a unit file; returns true when content changed.
func (s *Systemd) WriteUnit(name, content string) (bool, error) {
	return WriteIfChanged(filepath.Join(s.UnitDir, name), []byte(content), 0o644)
}

// WriteIfChanged writes data to path when different; returns changed.
func WriteIfChanged(path string, data []byte, perm os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// DaemonReload reloads unit definitions.
func (s *Systemd) DaemonReload(ctx context.Context) error {
	_, err := s.ctl(ctx, "daemon-reload")
	return err
}

// EnableRestart enables and (re)starts a unit.
func (s *Systemd) EnableRestart(ctx context.Context, unit string) error {
	if _, err := s.ctl(ctx, "enable", unit); err != nil {
		return err
	}
	_, err := s.ctl(ctx, "restart", unit)
	return err
}

// Reload sends SIGHUP-equivalent reload (falls back to restart).
func (s *Systemd) Reload(ctx context.Context, unit string) error {
	if _, err := s.ctl(ctx, "reload-or-restart", unit); err != nil {
		_, err = s.ctl(ctx, "restart", unit)
		return err
	}
	return nil
}

// StopDisable stops and disables a unit (ignores missing units).
func (s *Systemd) StopDisable(ctx context.Context, unit string) error {
	_, _ = s.ctl(ctx, "stop", unit)
	_, _ = s.ctl(ctx, "disable", unit)
	return nil
}

// IsActive reports whether the unit is running.
func (s *Systemd) IsActive(ctx context.Context, unit string) bool {
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	return strings.TrimSpace(string(out)) == "active"
}

// Show reads the properties needed for CoreStatus.
func (s *Systemd) Show(ctx context.Context, unit string) agentproto.CoreStatus {
	st := agentproto.CoreStatus{}
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "-p", "ActiveState,NRestarts,MemoryCurrent,ExecMainStartTimestamp,Result,CPUUsageNSec").Output()
	if err != nil {
		return st
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			st.Active = v == "active"
		case "NRestarts":
			st.NRestarts, _ = strconv.Atoi(v)
		case "MemoryCurrent":
			if v != "[not set]" && v != "infinity" {
				st.RSSBytes, _ = strconv.ParseInt(v, 10, 64)
			}
		case "ExecMainStartTimestamp":
			if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", v); err == nil {
				st.Since = t
			}
		case "Result":
			if v != "success" && v != "" {
				st.LastError = "result=" + v
			}
		}
	}
	return st
}

// IPAccounting returns cumulative bytes received/sent by a unit's cgroup.
// Ingress is VPS inbound, egress is VPS outbound. ok is false when unset.
func (s *Systemd) IPAccounting(ctx context.Context, unit string) (in, out int64, ok bool) {
	raw, err := exec.CommandContext(ctx, "systemctl", "show", unit, "-p", "IPIngressBytes", "-p", "IPEgressBytes").Output()
	if err != nil {
		return 0, 0, false
	}
	return parseIPAccounting(string(raw))
}

func parseIPAccounting(show string) (in, out int64, ok bool) {
	var haveIn, haveOut bool
	for _, line := range strings.Split(show, "\n") {
		k, v, okc := strings.Cut(line, "=")
		if !okc {
			continue
		}
		if v == "" || v == "[not set]" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			continue
		}
		switch k {
		case "IPIngressBytes":
			in, haveIn = n, true
		case "IPEgressBytes":
			out, haveOut = n, true
		}
	}
	return in, out, haveIn && haveOut
}

// JournalTail returns the last n log lines of a unit.
func (s *Systemd) JournalTail(ctx context.Context, unit string, n int) []string {
	out, err := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(n), "--no-pager", "-o", "cat", "-p", "warning").Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// ListUnits returns unit names matching a glob pattern.
func (s *Systemd) ListUnits(ctx context.Context, pattern string) []string {
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend", "--no-pager", pattern).Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && strings.HasSuffix(f[0], ".service") {
			names = append(names, f[0])
		}
	}
	return names
}

// ServiceUnit renders a hardened service unit.
func ServiceUnit(desc, execStart string, t agentproto.Tuning, extra ...string) string {
	memMax := t.MemoryMaxMB
	if memMax <= 0 {
		memMax = 256
	}
	nofile := t.LimitNOFILE
	if nofile <= 0 {
		nofile = 1048576
	}
	restart := t.RestartSec
	if restart <= 0 {
		restart = 3
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\nAfter=network-online.target nss-lookup.target\nWants=network-online.target\nStartLimitIntervalSec=0\n\n", desc)
	fmt.Fprintf(&b, "[Service]\nType=simple\nUser=root\nExecStart=%s\nRestart=always\nRestartSec=%d\nLimitNOFILE=%d\nMemoryMax=%dM\nCapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW CAP_SYS_PTRACE CAP_DAC_READ_SEARCH\nAmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW\nNoNewPrivileges=true\nProtectSystem=full\nProtectHome=true\nPrivateTmp=true\n", execStart, restart, nofile, memMax)
	for _, e := range extra {
		b.WriteString(e + "\n")
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}
