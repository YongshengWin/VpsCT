package networkguard

import (
	"fmt"
	"strings"
)

const AdmissionTable = "ctlvps_forward_admission"

func (p Plan) HasForwards() bool {
	for _, b := range p.Bindings {
		if b.ForwardID != 0 {
			return true
		}
	}
	return false
}

// AdmissionNeedsStop reports destructive replacement of a live connlimit.
// Recreating state cannot count old idle sockets, so root must terminate the
// public shared core before installing that replacement and renewing leases.
func (p Plan) AdmissionNeedsStop(old Plan) bool {
	limits := map[int64]struct{ tcp, udp, idle int }{}
	for _, b := range old.Bindings {
		if b.Forward != nil {
			limits[b.ForwardID] = struct{ tcp, udp, idle int }{b.Forward.MaxTCPConnections, b.Forward.MaxUDPSessions, b.Forward.UDPIdleSeconds}
		}
	}
	for _, b := range p.Bindings {
		if b.Forward != nil {
			if prior, exists := limits[b.ForwardID]; exists && prior != (struct{ tcp, udp, idle int }{b.Forward.MaxTCPConnections, b.Forward.MaxUDPSessions, b.Forward.UDPIdleSeconds}) {
				return true
			}
		}
	}
	return false
}

// AdmissionRules keeps each unchanged resource's stateful expression alive.
// Rebuilding dispatch rules or a lease table never resets its connlimit. The
// caller must fence/stop before rebuild=true or AdmissionNeedsStop returns true.
func (p Plan) AdmissionRules(old Plan, rebuild bool) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if err := old.Validate(); err != nil {
		return "", err
	}
	var s strings.Builder
	fmt.Fprintf(&s, "add table inet %s\n", AdmissionTable)
	if rebuild {
		fmt.Fprintf(&s, "delete table inet %s\nadd table inet %s\n", AdmissionTable, AdmissionTable)
		old.Bindings = nil
	}
	fmt.Fprintf(&s, "add chain inet %s input { type filter hook input priority -154; policy accept; }\nflush chain inet %s input\n", AdmissionTable, AdmissionTable)
	previous := map[int64]Binding{}
	for _, b := range old.Bindings {
		if b.Forward != nil && b.Forward.MaxTCPConnections > 0 {
			previous[b.ForwardID] = b
		}
	}
	for _, b := range p.sorted() {
		if b.Forward == nil || b.Forward.MaxTCPConnections == 0 {
			continue
		}
		chain := fmt.Sprintf("f%d_tcp", b.ForwardID)
		prior, exists := previous[b.ForwardID]
		fmt.Fprintf(&s, "add chain inet %s %s\n", AdmissionTable, chain)
		if !exists || prior.Forward.MaxTCPConnections != b.Forward.MaxTCPConnections {
			fmt.Fprintf(&s, "flush chain inet %s %s\nadd rule inet %s %s ct state new ct count over %d drop\n", AdmissionTable, chain, AdmissionTable, chain, b.Forward.MaxTCPConnections)
		}
		mark, _ := b.Resource().Mark()
		fmt.Fprintf(&s, "add rule inet %s input ct direction original tcp dport %d ct mark 0x%08x jump %s\n", AdmissionTable, b.ListenPort, mark, chain)
		delete(previous, b.ForwardID)
	}
	for _, b := range old.sorted() {
		if _, exists := previous[b.ForwardID]; !exists || b.Forward == nil {
			continue
		}
		fmt.Fprintf(&s, "flush chain inet %s f%d_tcp\ndelete chain inet %s f%d_tcp\n", AdmissionTable, b.ForwardID, AdmissionTable, b.ForwardID)
	}
	oldUDP := map[int64]Binding{}
	for _, b := range old.Bindings {
		if b.Forward != nil && b.Forward.MaxUDPSessions > 0 {
			oldUDP[b.ForwardID] = b
		}
	}
	for _, b := range p.sorted() {
		if b.Forward == nil || b.Forward.MaxUDPSessions == 0 {
			continue
		}
		chain := fmt.Sprintf("f%d_udp", b.ForwardID)
		prior, exists := oldUDP[b.ForwardID]
		changed := !exists || prior.Forward.MaxUDPSessions != b.Forward.MaxUDPSessions || prior.Forward.UDPIdleSeconds != b.Forward.UDPIdleSeconds
		fmt.Fprintf(&s, "add chain inet %s %s\n", AdmissionTable, chain)
		if changed {
			fmt.Fprintf(&s, "flush chain inet %s %s\n", AdmissionTable, chain)
			if exists {
				for _, suffix := range []string{"4", "6"} {
					fmt.Fprintf(&s, "delete ct timeout inet %s %s%s\n", AdmissionTable, chain, suffix)
				}
			}
			fmt.Fprintf(&s, "add rule inet %s %s ct state new ct count over %d drop\n", AdmissionTable, chain, b.Forward.MaxUDPSessions)
			for _, family := range []struct{ suffix, l3 string }{{"4", "ip"}, {"6", "ip6"}} {
				fmt.Fprintf(&s, "add ct timeout inet %s %s%s { protocol udp; l3proto %s; policy = { unreplied: %d, replied: %d }; }\n", AdmissionTable, chain, family.suffix, family.l3, b.Forward.UDPIdleSeconds, b.Forward.UDPIdleSeconds)
			}
		}
		mark, _ := b.Resource().Mark()
		for _, family := range []struct{ suffix, nf string }{{"4", "ipv4"}, {"6", "ipv6"}} {
			fmt.Fprintf(&s, "add rule inet %s input ct direction original meta nfproto %s udp dport %d ct mark 0x%08x ct timeout set \"%s%s\"\n", AdmissionTable, family.nf, b.ListenPort, mark, chain, family.suffix)
		}
		fmt.Fprintf(&s, "add rule inet %s input ct direction original udp dport %d ct mark 0x%08x jump %s\n", AdmissionTable, b.ListenPort, mark, chain)
		delete(oldUDP, b.ForwardID)
	}
	for _, b := range old.sorted() {
		if _, exists := oldUDP[b.ForwardID]; !exists || b.Forward == nil {
			continue
		}
		chain := fmt.Sprintf("f%d_udp", b.ForwardID)
		fmt.Fprintf(&s, "flush chain inet %s %s\ndelete chain inet %s %s\n", AdmissionTable, chain, AdmissionTable, chain)
		for _, suffix := range []string{"4", "6"} {
			fmt.Fprintf(&s, "delete ct timeout inet %s %s%s\n", AdmissionTable, chain, suffix)
		}
	}
	return s.String(), nil
}
