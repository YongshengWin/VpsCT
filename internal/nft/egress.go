package nft

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
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
	return EgressResourceRules(nodes, groups, resolvers, nil, "")
}
func EgressResourceRules(nodes []agentproto.NodeSpec, groups map[int64]string, resolvers []string, forwards []agentproto.ForwardSpec, forwardGroup string) (string, error) {
	var b strings.Builder
	// Reject invalid identities before the -140 accounting hook can charge them.
	b.WriteString("add table inet ctlvps_egress\nadd chain inet ctlvps_egress output { type filter hook output priority -150; policy accept; }\nflush chain inet ctlvps_egress output\n")
	fmt.Fprintf(&b, "add chain inet ctlvps_egress %s\n", networkguard.TransportChain)
	b.WriteString("add chain inet ctlvps_egress ctlvps_bootstrap\nflush chain inet ctlvps_egress ctlvps_bootstrap\nadd rule inet ctlvps_egress output meta mark & 0xff000000 { 0x44000000, 0x46000000 } jump ctlvps_bootstrap\nadd rule inet ctlvps_egress ctlvps_bootstrap meta skuid != 0 drop\nadd rule inet ctlvps_egress ctlvps_bootstrap fib daddr type local drop\n")
	nodes = append([]agentproto.NodeSpec(nil), nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	for _, n := range nodes {
		if n.Retired || n.Blocked || n.Core != "singbox" || n.Network == nil || !n.Network.HasTransport() {
			continue
		}
		cfg, _ := n.Network.TransportConfig()
		if err := cfg.Validate(); err != nil {
			return "", err
		}
		if _, err := networkconfig.HostAddress(cfg.Server); err == nil {
			continue
		}
		resolver := cfg.Outer.DNS
		ip, _ := networkconfig.HostAddress(resolver.Address)
		if networkconfig.NeedsTransportGrant(ip) {
			if !networkconfig.AuthorizeTransport(n.TransportGrants, n.NodeID, n.Network.Policy.EgressProfileID, "bootstrap_dns", "tcp", ip, resolver.Port) || (resolver.Transport == "udp" && !networkconfig.AuthorizeTransport(n.TransportGrants, n.NodeID, n.Network.Policy.EgressProfileID, "bootstrap_dns", "udp", ip, resolver.Port)) {
				continue
			}
		} else if (networkconfig.ResolvedSOCKS5{Address: ip.String(), Port: resolver.Port}).Validate() != nil {
			continue
		}
		mark, err := agentproto.BootstrapMark(n.NodeID)
		if err != nil {
			return "", err
		}
		family := "ip6"
		if ip.Is4() {
			family = "ip"
		}
		fmt.Fprintf(&b, "add rule inet ctlvps_egress ctlvps_bootstrap meta mark 0x%08x %s daddr %s tcp dport %d accept\n", mark, family, ip.String(), resolver.Port)
		if resolver.Transport == "udp" {
			fmt.Fprintf(&b, "add rule inet ctlvps_egress ctlvps_bootstrap meta mark 0x%08x %s daddr %s udp dport %d accept\n", mark, family, ip.String(), resolver.Port)
		}
	}
	for _, f := range forwards {
		if f.Blocked || f.Retired || f.Direct == nil {
			continue
		}
		if _, err := networkconfig.HostAddress(f.Config.TargetHost); err == nil {
			continue
		}
		dns := f.Direct.DNS
		ip, err := networkconfig.HostAddress(dns.Address)
		if err != nil {
			return "", err
		}
		if (networkconfig.ResolvedSOCKS5{Address: ip.String(), Port: dns.Port}).Validate() != nil {
			continue
		}
		mark, err := agentproto.ForwardBootstrapMark(f.ForwardID)
		if err != nil {
			return "", err
		}
		family := "ip"
		if ip.Is6() {
			family = "ip6"
		}
		fmt.Fprintf(&b, "add rule inet ctlvps_egress ctlvps_bootstrap meta mark 0x%08x %s daddr %s tcp dport %d accept\n", mark, family, ip.String(), dns.Port)
		if dns.Transport == "udp" {
			fmt.Fprintf(&b, "add rule inet ctlvps_egress ctlvps_bootstrap meta mark 0x%08x %s daddr %s udp dport %d accept\n", mark, family, ip.String(), dns.Port)
		}
	}
	b.WriteString("add rule inet ctlvps_egress ctlvps_bootstrap drop\n")
	protected, err := resourceEgressGroups(nodes, groups, forwards, forwardGroup)
	if err != nil {
		return "", err
	}
	for _, g := range protected {
		match := fmt.Sprintf("socket cgroupv2 level %d %q ct direction original", strings.Count(g.path, "/")+1, g.path)
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
		if g.core == "singbox" {
			if g.acme {
				g.marks = append(g.marks, "0x00000000")
			}
			if len(g.marks) == 0 {
				rule("drop")
			} else {
				rule("meta mark != { " + strings.Join(g.marks, ", ") + " } drop")
			}
		}
		if g.private {
			continue
		}
		rule("fib daddr type local drop")
		if g.core == "singbox" {
			rule("jump " + networkguard.TransportChain)
		}
		rule("ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4 } drop")
		rule("ip6 daddr { ::/128, ::1/128, ::ffff:0:0/96, 64:ff9b::/96, 64:ff9b:1::/48, 2001::/23, 2002::/16, fc00::/7, fe80::/10, ff00::/8 } drop")
	}
	return b.String(), nil
}
func (m *Manager) EnsureEgress(ctx context.Context, nodes []agentproto.NodeSpec, groups map[int64]string) error {
	return m.EnsureResourceEgress(ctx, nodes, groups, nil, "")
}
func (m *Manager) EnsureResourceEgress(ctx context.Context, nodes []agentproto.NodeSpec, groups map[int64]string, forwards []agentproto.ForwardSpec, forwardGroup string) error {
	var resolvers []string
	if b, e := os.ReadFile("/etc/resolv.conf"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
				resolvers = append(resolvers, f[1])
			}
		}
	}
	rules, e := EgressResourceRules(nodes, groups, resolvers, forwards, forwardGroup)
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
		} else if n.Core == "mieru" {
			unit = fmt.Sprintf("ctlvps-mita@%d.service", n.ListenPort)
		} else if n.Core == "snell" {
			unit = fmt.Sprintf("ctlvps-snell@%d.service", n.ListenPort)
		}
		if unit != "" && !seen[unit] {
			units = append(units, unit)
			seen[unit] = true
		}
	}
	for _, f := range forwards {
		if !f.Blocked && !f.Retired && !seen["ctlvps-singbox.service"] {
			units = append(units, "ctlvps-singbox.service")
			seen["ctlvps-singbox.service"] = true
		}
	}
	return proxyguard.InstallWithTransport(ctx, rules, units)
}
