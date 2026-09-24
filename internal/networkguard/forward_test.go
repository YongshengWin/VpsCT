package networkguard

import (
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

func TestForwardGuardCannotBorrowNodeReadinessOrTargetAuthority(t *testing.T) {
	p, snapshot := fixture(t)
	f := networkconfig.Forward{ListenMode: "all", ListenPort: 24444, Network: "tcp", TargetHost: "203.0.113.10", TargetPort: 443,
		SourceMode: "cidr", SourceCIDRs: []string{"192.0.2.2/32"}, MaxTCPConnections: 2, EgressProfileID: 1, EgressRevision: 1}
	b := p.Bindings[0]
	b.NodeID, b.ForwardID, b.ListenPort, b.Forward, b.Wanted = 0, 1, f.ListenPort, &f, f.BindingPolicy()
	p.Bindings = append(p.Bindings, b)
	node, forward := p.Bindings[0].Resource(), b.Resource()
	ready, bad := p.HealthyResources(snapshot, snapshot.SampledAt)
	if !ready[node] || !ready[forward] || len(bad) != 0 {
		t.Fatal(ready, bad)
	}
	legacy, err := p.Renew(map[int64]bool{1: true})
	if err != nil || strings.Contains(legacy, "add element inet ctlvps_network f1_ready") {
		t.Fatal("node readiness renewed forward", legacy, err)
	}
	revoked, err := p.RevokeResources(map[agentproto.ResourceIdentity]bool{forward: true})
	if err != nil || strings.Contains(revoked, "n1_ready") || !strings.Contains(revoked, "f1_ready") {
		t.Fatal(revoked, err)
	}
	rules, err := p.Rules()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ct mark set 0x45000001", "meta mark 0x45000001 meta nfproto != @f1_ready drop", "ip saddr != { 192.0.2.2/32 } drop", "ip daddr != { 192.0.2.2/32 } drop", "ip daddr != 203.0.113.10 drop", "tcp dport != 443 drop", "fib daddr type local drop"} {
		if !strings.Contains(rules, want) {
			t.Fatal("missing forward boundary", want)
		}
	}
	if strings.Contains(rules, "ct state established") || strings.Contains(rules, " accept\n") {
		t.Fatal("forward bypasses packet checks")
	}
	snapshot.Interfaces[0].Addresses = append(snapshot.Interfaces[0].Addresses, "203.0.113.10/24")
	ready, bad = p.HealthyResources(snapshot, snapshot.SampledAt)
	if ready[forward] || bad[forward] == "" || !ready[node] {
		t.Fatal("local target was not scoped to forward", ready, bad)
	}
	f.TargetHost = "10.0.0.2"
	if p.Validate() == nil {
		t.Fatal("private target borrowed a node's privileges")
	}
	b.Pending, b.Applied = true, networkconfig.Resolved{}
	p.Bindings[1] = b
	if p.Validate() != nil {
		t.Fatal("unsupported candidate could not be fenced")
	}
	renew, err := p.RenewResources(map[agentproto.ResourceIdentity]bool{forward: true})
	if err != nil || strings.Contains(renew, "add element inet ctlvps_network f1_ready") {
		t.Fatal("pending forward renewed", err)
	}
}

func TestForwardAdmissionOnlyResetsChangedLimits(t *testing.T) {
	p, _ := fixture(t)
	f := networkconfig.Forward{ListenMode: "all", ListenPort: 24444, Network: "tcp", TargetHost: "203.0.113.10", TargetPort: 443,
		SourceMode: "all", MaxTCPConnections: 2, EgressProfileID: 1, EgressRevision: 1}
	b := p.Bindings[0]
	b.NodeID, b.ForwardID, b.ListenPort, b.Forward, b.Wanted = 0, 1, f.ListenPort, &f, f.BindingPolicy()
	p.Bindings = append(p.Bindings, b)
	identical, err := p.AdmissionRules(p, false)
	if err != nil || strings.Contains(identical, "flush chain inet ctlvps_forward_admission f1_tcp") || strings.Contains(identical, "ct count") {
		t.Fatal("unchanged budget state was recreated", err, identical)
	}
	next := p
	next.Bindings = append([]Binding(nil), p.Bindings...)
	f.MaxTCPConnections++
	next.Bindings[1].Forward = &f
	// Make the old policy independent before changing the candidate.
	oldConfig := f
	oldConfig.MaxTCPConnections = 2
	p.Bindings[1].Forward = &oldConfig
	if !next.AdmissionNeedsStop(p) {
		t.Fatal("changed stateful limit did not require stopping old sockets")
	}
	if _, ok := ParseLeaseSetName("f1_ready"); !ok {
		t.Fatal("forward lease not recognized")
	}
	for _, name := range []string{"f01_ready", "f0_ready", "f16777216_ready", "x1_ready"} {
		if _, ok := ParseLeaseSetName(name); ok {
			t.Fatal("invalid lease name", name)
		}
	}
}

func TestUDPForwardAdmissionKeepsIdleBudgetState(t *testing.T) {
	p, _ := fixture(t)
	f := networkconfig.Forward{ListenMode: "all", ListenPort: 24444, Network: "udp", TargetHost: "203.0.113.10", TargetPort: 443,
		SourceMode: "all", MaxUDPSessions: 2, UDPIdleSeconds: 3, EgressProfileID: 1, EgressRevision: 1}
	b := p.Bindings[0]
	b.NodeID, b.ForwardID, b.ListenPort, b.Forward, b.Wanted = 0, 1, f.ListenPort, &f, f.BindingPolicy()
	p.Bindings = append(p.Bindings, b)
	rules, err := p.Rules()
	if err != nil || !strings.Contains(rules, "udp dport != 443 drop") || !strings.Contains(rules, "meta l4proto != udp drop") {
		t.Fatal(err, rules)
	}
	created, err := p.AdmissionRules(Plan{Token: p.Token}, true)
	if err != nil || !strings.Contains(created, "f1_udp4 { protocol udp; l3proto ip; policy = { unreplied: 3, replied: 3 }; }") ||
		!strings.Contains(created, "f1_udp6 { protocol udp; l3proto ip6; policy = { unreplied: 3, replied: 3 }; }") ||
		!strings.Contains(created, "ct count over 2 drop") {
		t.Fatal(err, created)
	}
	unchanged, err := p.AdmissionRules(p, false)
	if err != nil || strings.Contains(unchanged, "flush chain inet ctlvps_forward_admission f1_udp") || strings.Contains(unchanged, "add ct timeout") {
		t.Fatal("unchanged UDP admission was reset", err, unchanged)
	}
	next := p
	next.Bindings = append([]Binding(nil), p.Bindings...)
	changed := f
	changed.UDPIdleSeconds = 4
	next.Bindings[1].Forward = &changed
	if !next.AdmissionNeedsStop(p) {
		t.Fatal("UDP idle change did not stop old sessions")
	}
}

func TestForwardDNSPinExpiresAndCannotChangeAuthorityOnRenewal(t *testing.T) {
	p, snapshot := fixture(t)
	f := networkconfig.Forward{ListenMode: "all", ListenPort: 24444, Network: "tcp", TargetHost: "forward.example", TargetPort: 443, SourceMode: "all", MaxTCPConnections: 2, EgressProfileID: 1, EgressRevision: 1}
	b := p.Bindings[0]
	b.NodeID, b.ForwardID, b.ListenPort, b.Forward, b.Wanted = 0, 1, f.ListenPort, &f, f.BindingPolicy()
	pin := networkconfig.ResolvedForwardTarget{ResolvedSOCKS5: networkconfig.ResolvedSOCKS5{Host: f.TargetHost, Address: "203.0.113.10", Port: 443}}
	pin.SetDNSLifetime(snapshot.SampledAt, 60)
	b.Applied.ForwardTarget = &pin
	p.Bindings = []Binding{b}
	ready, bad := p.HealthyResources(snapshot, snapshot.SampledAt)
	if !ready[b.Resource()] || len(bad) != 0 {
		t.Fatal(ready, bad)
	}
	next := p
	next.Bindings = append([]Binding(nil), p.Bindings...)
	renewed := pin
	renewed.SetDNSLifetime(snapshot.SampledAt.Add(30*time.Second), 60)
	next.Bindings[0].Applied.ForwardTarget = &renewed
	if !p.SamePaths(next) {
		t.Fatal("same endpoint renewal changes path")
	}
	renewed.Address = "203.0.113.11"
	if p.SamePaths(next) {
		t.Fatal("changed DNS endpoint retained old authority")
	}
	snapshot.SampledAt = snapshot.SampledAt.Add(61 * time.Second)
	ready, bad = p.HealthyResources(snapshot, snapshot.SampledAt)
	if ready[b.Resource()] || bad[b.Resource()] == "" {
		t.Fatal("expired endpoint renewed")
	}
	pin.Address = "10.0.0.1"
	if p.Validate() == nil {
		t.Fatal("private DNS answer gained authority")
	}
}
