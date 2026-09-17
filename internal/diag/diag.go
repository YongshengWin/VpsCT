// Package diag collects host health facts that explain "why is my node
// flaky": congestion control, time sync, dual-stack reachability, OOM kills.
package diag

import (
	"context"
	"ctlvps/internal/boundedexec"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Host is a snapshot of host-level diagnostics.
type Host struct {
	CongestionCtl string
	BBR           bool
	TimeSync      bool
	IPv4Reachable bool
	IPv6Reachable bool
	OOMEvents     int
	Nftables      bool
	Systemd       bool
	Warnings      []string
}

func read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func reachable(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Collect gathers diagnostics (best effort, never fails).
func Collect(ctx context.Context) Host {
	h := Host{}
	h.CongestionCtl = read("/proc/sys/net/ipv4/tcp_congestion_control")
	h.BBR = h.CongestionCtl == "bbr"
	if out, err := exec.CommandContext(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value").Output(); err == nil {
		h.TimeSync = strings.TrimSpace(string(out)) == "yes"
	} else if exec.CommandContext(ctx, "chronyc", "tracking").Run() == nil {
		h.TimeSync = true
	}
	h.IPv4Reachable = reachable(ctx, "1.1.1.1:443") || reachable(ctx, "8.8.8.8:443")
	h.IPv6Reachable = reachable(ctx, "[2606:4700:4700::1111]:443") || reachable(ctx, "[2001:4860:4860::8888]:443")
	h.Nftables = exec.CommandContext(ctx, "nft", "--version").Run() == nil
	h.Systemd = exec.CommandContext(ctx, "systemctl", "--version").Run() == nil
	if out, _, err := boundedexec.Run(ctx, "", 256<<10, "journalctl", "-k", "--since", "-24h", "--no-pager", "-o", "cat", "--grep", "(?i)out of memory|oom-kill", "--lines", "1000"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			l := strings.ToLower(line)
			if strings.Contains(l, "out of memory") || strings.Contains(l, "oom-kill") {
				h.OOMEvents++
			}
		}
	}
	if !h.BBR {
		h.Warnings = append(h.Warnings, "拥塞控制不是 BBR ("+h.CongestionCtl+")")
	}
	if !h.TimeSync {
		h.Warnings = append(h.Warnings, "系统时间未同步（影响 Reality/Hysteria2 握手）")
	}
	if !h.IPv4Reachable {
		h.Warnings = append(h.Warnings, "IPv4 出网不可达")
	}
	if h.OOMEvents > 0 {
		h.Warnings = append(h.Warnings, "24h 内发生 "+strconv.Itoa(h.OOMEvents)+" 次 OOM")
	}
	if !h.Nftables {
		h.Warnings = append(h.Warnings, "nftables 不可用，无法按端口计量")
	}
	return h
}

// EnableBBR sets bbr + fq via sysctl.d (idempotent). Returns changed.
func EnableBBR(ctx context.Context) (bool, error) {
	if read("/proc/sys/net/ipv4/tcp_congestion_control") == "bbr" && read("/proc/sys/net/core/default_qdisc") == "fq" {
		return false, nil
	}
	content := "net.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n"
	path := "/etc/sysctl.d/99-ctlvps-bbr.conf"
	if old, err := os.ReadFile(path); err != nil || string(old) != content {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return false, err
		}
	}
	_ = exec.CommandContext(ctx, "modprobe", "tcp_bbr").Run()
	if out, err := exec.CommandContext(ctx, "sysctl", "-p", path).CombinedOutput(); err != nil {
		return false, &sysctlError{msg: strings.TrimSpace(string(out))}
	}
	return true, nil
}

type sysctlError struct{ msg string }

func (e *sysctlError) Error() string { return "sysctl: " + e.msg }

// EnsureChrony makes sure a time daemon is enabled (best effort).
func EnsureChrony(ctx context.Context) {
	for _, unit := range []string{"chrony", "chronyd", "systemd-timesyncd"} {
		if exec.CommandContext(ctx, "systemctl", "is-enabled", unit).Run() == nil {
			_ = exec.CommandContext(ctx, "systemctl", "start", unit).Run()
			return
		}
	}
	for _, unit := range []string{"chrony", "chronyd", "systemd-timesyncd"} {
		if exec.CommandContext(ctx, "systemctl", "enable", "--now", unit).Run() == nil {
			return
		}
	}
}

// PublicIPs discovers public addresses via local interfaces (non-private).
func PublicIPs() (v4, v6 string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				if v4 == "" {
					v4 = ip4.String()
				}
			} else if v6 == "" {
				v6 = ip.String()
			}
		}
	}
	return v4, v6
}
