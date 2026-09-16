package nft

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/proxyguard"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
)

// EgressRules is separate from accounting. It matches outbound proxy sockets,
// never the agent or controller, and never blocks replies to proxy clients.
func EgressRules(nodes []agentproto.NodeSpec, groups map[int64]string, resolvers []string) (string, error) {
	var b strings.Builder
	// Reject invalid identities before the -140 accounting hook can charge them.
	b.WriteString("add table inet ctlvps_egress\nadd chain inet ctlvps_egress output { type filter hook output priority -150; policy accept; }\nflush chain inet ctlvps_egress output\n")
	seen := map[string]bool{}
	nodes = append([]agentproto.NodeSpec(nil), nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	for _, n := range nodes {
		if n.Retired {
			continue
		}
		var match string
		switch n.Core {
		case "singbox", "snell":
			g := strings.TrimPrefix(groups[n.NodeID], "/")
			if g == "" || strings.Contains(g, "..") || strings.ContainsAny(g, "\"\\\r\n ") {
				return "", fmt.Errorf("missing or invalid proxy cgroup")
			}
			match = fmt.Sprintf("socket cgroupv2 level %d %q", strings.Count(g, "/")+1, g)
		default:
			return "", fmt.Errorf("invalid proxy core")
		}
		if seen[match] {
			continue
		}
		seen[match] = true
		// Return traffic on accepted client sockets must remain available.
		match += " ct direction original"
		rule := func(expr string) { fmt.Fprintf(&b, "add rule inet ctlvps_egress output %s %s\n", match, expr) }
		for _, raw := range resolvers {
			ip := net.ParseIP(raw)
			if ip == nil {
				return "", fmt.Errorf("invalid local DNS resolver")
			}
			family := "ip6"
			if ip.To4() != nil {
				family = "ip"
			}
			rule(fmt.Sprintf("%s daddr %s meta l4proto { tcp, udp } th dport 53 accept", family, ip.String()))
		}
		if n.Core == "singbox" {
			marks := []string{}
			acme := false
			for _, peer := range nodes {
				if peer.Retired || groups[peer.NodeID] != groups[n.NodeID] {
					continue
				}
				if peer.Core != n.Core || peer.AllowPrivate != n.AllowPrivate {
					return "", fmt.Errorf("inconsistent proxy permission group")
				}
				if peer.Blocked {
					continue
				}
				mark, e := NodeMark(peer.NodeID)
				if e != nil {
					return "", e
				}
				marks = append(marks, fmt.Sprintf("0x%08x", mark))
				acme = acme || (peer.Cert != nil && peer.Cert.Mode == "acme")
			}
			// Native ACME's own HTTPS traffic has no node mark. Preserve that
			// feature explicitly; marks remain untrusted within a shared process.
			if acme {
				marks = append(marks, "0x00000000")
			}
			if len(marks) == 0 {
				rule("drop")
			} else {
				rule("meta mark != { " + strings.Join(marks, ", ") + " } drop")
			}
		}
		if n.AllowPrivate {
			continue
		}
		rule("fib daddr type local drop")
		rule("ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4 } drop")
		rule("ip6 daddr { ::/128, ::1/128, ::ffff:0:0/96, 64:ff9b::/96, 64:ff9b:1::/48, 2001::/23, 2002::/16, fc00::/7, fe80::/10, ff00::/8 } drop")
	}
	return b.String(), nil
}
func (m *Manager) EnsureEgress(ctx context.Context, nodes []agentproto.NodeSpec, groups map[int64]string) error {
	var resolvers []string
	if b, e := os.ReadFile("/etc/resolv.conf"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
				resolvers = append(resolvers, f[1])
			}
		}
	}
	rules, e := EgressRules(nodes, groups, resolvers)
	if e != nil {
		return e
	}
	units := []string{}
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Retired || n.Blocked {
			continue
		}
		var unit string
		if n.Core == "singbox" {
			unit = "ctlvps-singbox.service"
			if n.AllowPrivate {
				unit = "ctlvps-singbox-private.service"
			}
		} else if n.Core == "snell" {
			unit = fmt.Sprintf("ctlvps-snell@%d.service", n.ListenPort)
		}
		if unit != "" && !seen[unit] {
			units = append(units, unit)
			seen[unit] = true
		}
	}
	return proxyguard.Install(ctx, rules, units)
}
