package networkguard

import (
	"fmt"
	"net/netip"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

// Rules starts with empty leases. It runs before the general egress/DNS and
// accounting hooks, including for established connections. Reinstalling it can
// only block until fresh local observations renew a matching plan token.
func (p Plan) Rules() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	var s strings.Builder
	fmt.Fprintf(&s, "add table inet %s\ndelete table inet %s\nadd table inet %s\n", Table, Table, Table)
	for _, chain := range []string{"input", "output"} {
		fmt.Fprintf(&s, "add chain inet %s %s { type filter hook %s priority -155; policy accept; }\n", Table, chain, chain)
	}
	// Forward client and target sockets acquire identity before the lease
	// checks, independently of the later accounting table.
	for _, b := range p.sorted() {
		if b.ForwardID == 0 {
			continue
		}
		mark, _ := b.Resource().Mark()
		fmt.Fprintf(&s, "add rule inet %s input ct direction original meta l4proto { tcp, udp } th dport %d ct mark set 0x%08x\n", Table, b.ListenPort, mark)
	}
	fmt.Fprintf(&s, "add rule inet %s output meta mark & 0xff000000 == 0x%08x ct mark set meta mark\n", Table, agentproto.ForwardMarkPrefix)
	for _, b := range p.sorted() {
		set := ResourceSetName(b.Resource())
		fmt.Fprintf(&s, "add set inet %s %s { type nf_proto; flags timeout; timeout 6s; gc-interval 1s; size 2; }\n", Table, set)
		rule := func(chain, match, action string) {
			fmt.Fprintf(&s, "add rule inet %s %s %s %s\n", Table, chain, match, action)
		}
		port := fmt.Sprintf("ct direction original meta l4proto { tcp, udp } th dport %d", b.ListenPort)
		gate := "meta nfproto != @" + set + " drop"
		rule("input", port, gate)
		if b.Forward != nil {
			// ACL runs for every packet, including existing flows. No accept
			// here may skip the interface, budget or general proxy hooks.
			allowed := "tcp"
			if b.Forward.Network == "udp" {
				allowed = "udp"
			} else if b.Forward.Network == "both" {
				allowed = "{ tcp, udp }"
			}
			rule("input", port, "meta l4proto != "+allowed+" drop")
			if b.Forward.SourceMode == "cidr" {
				for _, family := range []struct {
					nf, ip string
					v4     bool
				}{{"ipv4", "ip", true}, {"ipv6", "ip6", false}} {
					var cidrs []string
					for _, cidr := range b.Forward.SourceCIDRs {
						p, _ := netip.ParsePrefix(cidr)
						if p.Addr().Is4() == family.v4 {
							cidrs = append(cidrs, p.String())
						}
					}
					match := port + " meta nfproto " + family.nf
					mark, _ := b.Resource().Mark()
					reply := fmt.Sprintf("ct mark 0x%08x ct direction reply meta nfproto %s", mark, family.nf)
					if len(cidrs) == 0 {
						rule("input", match, "drop")
						rule("output", reply, "drop")
					} else {
						rule("input", match, family.ip+" saddr != { "+strings.Join(cidrs, ", ")+" } drop")
						rule("output", reply, family.ip+" daddr != { "+strings.Join(cidrs, ", ")+" } drop")
					}
				}
			}
		}
		if b.Applied.ListenInterface != nil {
			a, _ := networkconfig.HostAddress(b.Applied.ListenAddress)
			family, proto := "ip6", "ipv6"
			if a.Is4() {
				family, proto = "ip", "ipv4"
			}
			rule("input", port, "meta nfproto != "+proto+" drop")
			rule("input", port, family+" daddr != "+a.String()+" drop")
			rule("input", port, fmt.Sprintf("meta iif != %d drop", b.Applied.ListenInterface.Index))
		}
		if b.Core == "snell" || b.Core == "mieru" {
			g := strings.TrimPrefix(b.Group, "/")
			rule("input", fmt.Sprintf("socket cgroupv2 level %d %q", strings.Count(g, "/")+1, g), gate)
			rule("output", fmt.Sprintf("socket cgroupv2 level %d %q", strings.Count(g, "/")+1, g), gate)
			continue
		}
		mark, _ := b.Resource().Mark()
		for _, chain := range []string{"input", "output"} {
			rule(chain, fmt.Sprintf("ct mark 0x%08x", mark), gate)
		}
		socket := fmt.Sprintf("meta mark 0x%08x", mark)
		rule("output", socket, gate)
		original := socket + " ct direction original"
		if b.Forward != nil && !b.Pending && b.Applied.SOCKS5 == nil {
			ip, _ := b.Forward.ResolvedTarget(b.Applied.ForwardTarget)
			family, nf := "ip6", "ipv6"
			if ip.Is4() {
				family, nf = "ip", "ipv4"
			}
			rule("output", original, "fib daddr type local drop")
			rule("output", original, "meta nfproto != "+nf+" drop")
			rule("output", original, family+" daddr != "+ip.String()+" drop")
			allowed := "tcp"
			if b.Forward.Network == "udp" {
				allowed = "udp"
			} else if b.Forward.Network == "both" {
				allowed = "{ tcp, udp }"
			}
			rule("output", original, "meta l4proto != "+allowed+" drop")
			portMatch := "tcp"
			if b.Forward.Network == "udp" {
				portMatch = "udp"
			} else if b.Forward.Network == "both" {
				portMatch = "th"
			}
			rule("output", original, fmt.Sprintf("%s dport != %d drop", portMatch, b.Forward.TargetPort))
		}
		d := b.Applied.Direct
		if d == nil {
			continue
		}
		if d.Interface != nil {
			rule("output", original, fmt.Sprintf("meta oif != %d drop", d.Interface.Index))
		}
		if d.Config.Family == "ipv4" {
			rule("output", original, "meta nfproto != ipv4 drop")
		}
		if d.Config.Family == "ipv6" {
			rule("output", original, "meta nfproto != ipv6 drop")
		}
		for _, field := range []struct {
			family string
			source *networkconfig.Address
		}{{"ip", d.Config.SourceIPv4}, {"ip6", d.Config.SourceIPv6}} {
			family, source := field.family, field.source
			if source != nil {
				a, _ := networkconfig.HostAddress(source.Address)
				rule("output", original, family+" saddr != "+a.String()+" drop")
			}
		}
		if endpoint := b.Applied.SOCKS5; endpoint != nil {
			ip, _ := networkconfig.HostAddress(endpoint.Address)
			family := "ip6"
			if ip.Is4() {
				family = "ip"
			}
			// return only exits this guard hook; the general cgroup/private
			// policy and accounting hooks still run. Client replies are excluded.
			target := family + " daddr " + ip.String()
			if endpoint.TCPAllowed() {
				rule("output", original, fmt.Sprintf("%s tcp dport %d return", target, endpoint.Port))
			}
			for _, ports := range endpoint.UDPPorts() {
				rule("output", original, fmt.Sprintf("%s udp dport %s return", target, transportPorts(ports)))
			}
			rule("output", original, "drop")
		}
	}
	// No old forward socket can survive removal of its resource from the plan.
	var marks []string
	for _, b := range p.sorted() {
		if b.ForwardID != 0 {
			mark, _ := b.Resource().Mark()
			marks = append(marks, fmt.Sprintf("0x%08x", mark))
		}
	}
	for _, chain := range []string{"input", "output"} {
		match := fmt.Sprintf("ct mark & 0xff000000 == 0x%08x", agentproto.ForwardMarkPrefix)
		if len(marks) > 0 {
			match += " ct mark != { " + strings.Join(marks, ", ") + " }"
		}
		fmt.Fprintf(&s, "add rule inet %s %s %s drop\n", Table, chain, match)
	}
	return s.String(), nil
}

const TransportChain = "ctlvps_transports"

func transportPorts(ports networkconfig.TransportPortRange) string {
	if ports.First == ports.Last {
		return fmt.Sprint(ports.First)
	}
	return fmt.Sprintf("%d-%d", ports.First, ports.Last)
}

// TransportRules is an exception chain in the general proxy firewall. Its
// source is the applied root plan, never an un-applied controller request or a
// whole private CIDR. The earlier network hook still requires a live lease.
func (p Plan) TransportRules() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	var s strings.Builder
	fmt.Fprintf(&s, "add chain inet ctlvps_egress %s\nflush chain inet ctlvps_egress %s\n", TransportChain, TransportChain)
	fmt.Fprintf(&s, "add rule inet ctlvps_egress %s fib daddr type local drop\n", TransportChain)
	for _, binding := range p.sorted() {
		if pin := binding.Applied.ForwardTarget; !binding.Pending && !binding.ForwardTransport && pin != nil && len(pin.Grants) > 0 {
			ip, _ := networkconfig.HostAddress(pin.Address)
			family := "ip6"
			if ip.Is4() {
				family = "ip"
			}
			mark, _ := binding.Resource().Mark()
			protocol := "tcp"
			if binding.Forward != nil {
				protocol = binding.Forward.Network
			}
			if protocol == "both" {
				protocol = "meta l4proto { tcp, udp } th"
			}
			fmt.Fprintf(&s, "add rule inet ctlvps_egress %s meta mark 0x%08x ct direction original %s daddr %s %s dport %d accept\n", TransportChain, mark, family, ip.String(), protocol, pin.Port)
		}
		endpoint := binding.Applied.SOCKS5
		if binding.Pending || endpoint == nil || len(endpoint.TransportGrants) == 0 {
			continue
		}
		ip, _ := networkconfig.HostAddress(endpoint.Address)
		family := "ip6"
		if ip.Is4() {
			family = "ip"
		}
		mark, _ := binding.Resource().Mark()
		prefix := fmt.Sprintf("add rule inet ctlvps_egress %s meta mark 0x%08x ct direction original %s daddr %s", TransportChain, mark, family, ip.String())
		if endpoint.TCPAllowed() {
			fmt.Fprintf(&s, "%s tcp dport %d accept\n", prefix, endpoint.Port)
		}
		for _, ports := range endpoint.UDPPorts() {
			fmt.Fprintf(&s, "%s udp dport %s accept\n", prefix, transportPorts(ports))
		}
	}
	return s.String(), nil
}

// Renew replaces every lease atomically. No packet-path rule can renew leases.
func (p Plan) RenewResources(ready map[agentproto.ResourceIdentity]bool) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	var s strings.Builder
	for _, b := range p.sorted() {
		set := ResourceSetName(b.Resource())
		fmt.Fprintf(&s, "flush set inet %s %s\n", Table, set)
		if ready[b.Resource()] && !b.Pending {
			fmt.Fprintf(&s, "add element inet %s %s { ipv4 timeout 6s, ipv6 timeout 6s }\n", Table, set)
		}
	}
	return s.String(), nil
}

// Revoke only touches affected nodes and never extends another node's lease.
func (p Plan) RevokeResources(affected map[agentproto.ResourceIdentity]bool) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	var s strings.Builder
	for _, b := range p.sorted() {
		if affected[b.Resource()] {
			fmt.Fprintf(&s, "flush set inet %s %s\n", Table, ResourceSetName(b.Resource()))
		}
	}
	return s.String(), nil
}
