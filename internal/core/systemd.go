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
	"ctlvps/internal/boundedexec"
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
	out, stderr, err := boundedexec.Run(ctx, "", 1<<20, "systemctl", args...)
	if err != nil {
		return "", fmt.Errorf("systemctl: %w: %s", err, strings.TrimSpace(stderr))
	}
	return string(out), nil
}

// WriteUnit writes a unit file; returns true when content changed.
func (s *Systemd) WriteUnit(name, content string) (bool, error) {
	if !strings.HasPrefix(name, "ctlvps-") || strings.Contains(name, "..") || strings.ContainsAny(name, "\\\r\n\x00") || filepath.IsAbs(name) {
		return false, fmt.Errorf("invalid managed unit name")
	}
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
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return false, fmt.Errorf("target is not a regular file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ctlvps-write-")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(perm); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return false, err
	}
	if ce != nil {
		return false, ce
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
	if _, err := s.ctl(ctx, "stop", unit); err != nil {
		return err
	}
	_, err := s.ctl(ctx, "disable", unit)
	return err
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
	fmt.Fprintf(&b, "[Service]\nType=simple\nUser=root\nSlice=ctlvps-proxy.slice\nExecStart=%s\nRestart=always\nRestartSec=%d\nLimitNOFILE=%d\nMemoryMax=%dM\nCapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW\nAmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW\nNoNewPrivileges=true\nProtectSystem=strict\nReadWritePaths=-/var/log/ctlvps -/var/lib/ctlvps-agent\nProtectHome=true\nPrivateTmp=true\nProtectKernelTunables=true\nProtectControlGroups=true\nRestrictSUIDSGID=true\nRestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK\nLogRateLimitIntervalSec=30s\nLogRateLimitBurst=100\n", execStart, restart, nofile, memMax)
	for _, e := range extra {
		b.WriteString(e + "\n")
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// AccountingSnapshot reads every requested unit in one process invocation.
// InvocationID distinguishes service restarts from a counter rollback.
type AccountingReading struct {
	Rx, Tx int64
	Epoch  string
	Valid  bool
}

func (s *Systemd) AccountingSnapshot(ctx context.Context, units []string) (map[string]AccountingReading, error) {
	out := map[string]AccountingReading{}
	if len(units) == 0 {
		return out, nil
	}
	args := append([]string{"show", "-p", "Id,InvocationID,IPIngressBytes,IPEgressBytes"}, units...)
	raw, err := s.ctl(ctx, args...)
	if err != nil {
		return nil, err
	}
	for _, block := range strings.Split(strings.TrimSpace(raw), "\n\n") {
		id, epoch := "", ""
		for _, l := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(l, "=")
			if k == "Id" {
				id = v
			}
			if k == "InvocationID" {
				epoch = v
			}
		}
		rx, tx, ok := parseIPAccounting(block)
		if id != "" {
			out[id] = AccountingReading{Rx: rx, Tx: tx, Epoch: epoch, Valid: ok && epoch != ""}
		}
	}
	return out, nil
}

// Stop preserves unit accounting until the next invocation starts.
func (s *Systemd) StopUnits(ctx context.Context, units []string) error {
	if len(units) == 0 {
		return nil
	}
	_, err := s.ctl(ctx, append([]string{"stop"}, units...)...)
	return err
}

func (s *Systemd) StartUnits(ctx context.Context, units []string) error {
	if len(units) == 0 {
		return nil
	}
	_, err := s.ctl(ctx, append([]string{"start"}, units...)...)
	return err
}

// SnellSlice stays active across child service restarts and preserves counters.
func SnellSlice(nodeID int64) string { return fmt.Sprintf("ctlvps-proxy-n%d.slice", nodeID) }

func (s *Systemd) EnsureSnellMeter(ctx context.Context, n agentproto.NodeSpec) (bool, error) {
	if n.NodeID <= 0 {
		return false, fmt.Errorf("invalid Snell node identity")
	}
	name := SnellSlice(n.NodeID)
	changed, err := s.WriteUnit(name, "[Unit]\nDescription=VpsCT node accounting\n[Slice]\nIPAccounting=yes\n")
	if err != nil {
		return false, err
	}
	dropin, err := s.WriteUnit(StandaloneUnit(n.Core, n.ListenPort)+".d/meter.conf", "[Service]\nSlice="+name+"\n")
	if err != nil {
		return false, err
	}
	if changed || dropin {
		if err = s.DaemonReload(ctx); err != nil {
			return false, err
		}
	}
	return changed || dropin, s.StartUnits(ctx, []string{name})
}

// EnsureProxyBudget bounds the aggregate of all proxy processes, including
// official Snell instances. The agent is deliberately outside this slice.
func (s *Systemd) EnsureProxyBudget(ctx context.Context, t agentproto.Tuning) error {
	limit := t.MemoryMaxMB
	if limit <= 0 {
		limit = 256
	}
	unit := fmt.Sprintf("[Unit]\nDescription=VpsCT proxy resource budget\n[Slice]\nMemoryAccounting=yes\nMemoryHigh=%dM\nMemoryMax=%dM\n", limit*3/4, limit)
	changed, err := s.WriteUnit("ctlvps-proxy.slice", unit)
	if err != nil {
		return err
	}
	if changed {
		return s.DaemonReload(ctx)
	}
	return nil
}

func (s *Systemd) InSlice(ctx context.Context, unit, slice string) bool {
	out, err := s.ctl(ctx, "show", unit, "--property=Slice", "--value")
	return err == nil && strings.TrimSpace(out) == slice
}

// ControlGroup resolves an existing managed slice, never a panel-provided path.
func (s *Systemd) ControlGroup(ctx context.Context, unit string) (string, error) {
	out, e := s.ctl(ctx, "show", unit, "--property=ControlGroup", "--value")
	if e != nil {
		return "", e
	}
	g := strings.TrimSpace(out)
	if !strings.HasPrefix(g, "/") || !strings.Contains(g, "ctlvps-proxy") {
		return "", fmt.Errorf("managed cgroup unavailable")
	}
	return g, nil
}

// EmptySnellMeter refuses to retire a slice while any child still has tasks.
// Unlike the port-named service, the slice identity cannot be reused by a new
// node on the same listening port.
func (s *Systemd) EmptySnellMeter(ctx context.Context, nodeID int64) error {
	if nodeID <= 0 {
		return fmt.Errorf("invalid meter ID")
	}
	g, err := s.ControlGroup(ctx, SnellSlice(nodeID))
	if err != nil {
		return err
	}
	if filepath.Clean(g) != g || strings.Contains(g, "..") {
		return fmt.Errorf("invalid managed cgroup")
	}
	raw, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", g, "cgroup.events"))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "populated 0" {
			return nil
		}
	}
	return fmt.Errorf("retired Snell slice still populated")
}
func (s *Systemd) FinalSnellReading(ctx context.Context, nodeID int64) (AccountingReading, error) {
	if err := s.EmptySnellMeter(ctx, nodeID); err != nil {
		return AccountingReading{}, err
	}
	unit := SnellSlice(nodeID)
	out, err := s.AccountingSnapshot(ctx, []string{unit})
	if err != nil {
		return AccountingReading{}, err
	}
	r := out[unit]
	if !r.Valid {
		return r, fmt.Errorf("final Snell counter unavailable")
	}
	return r, nil
}
func (s *Systemd) RemoveSnellMeter(ctx context.Context, nodeID int64) error {
	if nodeID <= 0 {
		return fmt.Errorf("invalid meter ID")
	}
	unit := SnellSlice(nodeID)
	if s.IsActive(ctx, unit) {
		if err := s.EmptySnellMeter(ctx, nodeID); err != nil {
			return err
		}
		if err := s.StopUnits(ctx, []string{unit}); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(s.UnitDir, unit)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.DaemonReload(ctx)
}
