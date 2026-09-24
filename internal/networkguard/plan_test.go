package networkguard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
)

func fixture(t *testing.T) (Plan, *agentproto.NetworkSnapshot) {
	t.Helper()
	id := "11111111111111111111111111111111"
	s := &agentproto.NetworkSnapshot{Version: 1, CollectorID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BootID: "fixture", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "wan1", Index: 2, Kind: "ether", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}
	n := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}
	d := networkconfig.Direct{InterfaceID: id, Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}
	r, err := netinventory.ResolveBinding(n, &d, s, false, s.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New([]Binding{{NodeID: 1, ListenPort: 24443, Core: "singbox", Wanted: n, Applied: r}})
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}

func TestGuardRequiresSameAppliedIdentityAndFreshObservation(t *testing.T) {
	p, s := fixture(t)
	s.Sequence++
	// A counter baseline reset is independent of the device identity. A
	// provably unchanged physical interface may resume after agent restart.
	s.Interfaces[0].Generation = "cccccccccccccccccccccccccccccccc"
	good, bad := p.Healthy(s, s.SampledAt)
	if !good[1] || len(bad) != 0 {
		t.Fatal(good, bad)
	}
	for _, edit := range []func(*agentproto.NetworkSnapshot){
		func(s *agentproto.NetworkSnapshot) { s.Interfaces[0].Name = "renamed0" },
		func(s *agentproto.NetworkSnapshot) { s.Interfaces[0].ID = "22222222222222222222222222222222" },
		func(s *agentproto.NetworkSnapshot) { s.Interfaces[0].Up = false },
		func(s *agentproto.NetworkSnapshot) { s.CollectorID = "cccccccccccccccccccccccccccccccc" },
		func(s *agentproto.NetworkSnapshot) { s.BootID = "other-boot" },
		func(s *agentproto.NetworkSnapshot) { s.SampledAt = s.SampledAt.Add(-2 * time.Second) },
	} {
		p, s := fixture(t)
		now := s.SampledAt
		edit(s)
		good, bad := p.Healthy(s, now)
		if good[1] || bad[1] == "" {
			t.Fatal("invalid binding remained healthy")
		}
	}
	if len(p.Affected(map[int]bool{3: true}, false)) != 0 || !p.Affected(map[int]bool{2: true}, false)[1] || !p.Affected(nil, true)[1] {
		t.Fatal("event scope differs from interface dependencies")
	}
}

func TestGuardDefaultsClosedAndRevokeDoesNotRenewPeers(t *testing.T) {
	p, _ := fixture(t)
	rules, err := p.Rules()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rules, "add element") || !strings.Contains(rules, "priority -155") || !strings.Contains(rules, "meta mark 0x43000001 meta nfproto != @n1_ready drop") || !strings.Contains(rules, "meta oif != 2 drop") {
		t.Fatal(rules)
	}
	for i := 0; i < 25; i++ {
		same, _ := p.Rules()
		if same != rules {
			t.Fatal("non-deterministic guard rules")
		}
	}
	renew, err := p.Renew(map[int64]bool{1: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(renew, "timeout 6s") {
		t.Fatal("missing bounded lease")
	}
	revoke, err := p.Revoke(map[int64]bool{1: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(revoke, "add element") || !strings.Contains(revoke, "flush set") {
		t.Fatal("revoke extended a lease")
	}
	p.Bindings[0].Applied.Direct.Interface.Index = 0
	if _, err = p.Rules(); err == nil {
		t.Fatal("unresolved binding allowed")
	}
}

func TestSOCKS5DNSExpiryAndTimingOnlyRenewal(t *testing.T) {
	p, s := fixture(t)
	now := s.SampledAt
	pin := &networkconfig.ResolvedSOCKS5{Host: "upstream.example", Address: "192.0.2.2", Port: 1080}
	pin.SetDNSLifetime(now, 60)
	p.Bindings[0].Applied.SOCKS5 = pin
	for _, offset := range []time.Duration{0, 31 * time.Second, 60 * time.Second, -time.Second} {
		s.SampledAt = now.Add(offset)
		good, bad := p.Healthy(s, s.SampledAt)
		want := offset >= 0 && offset < 60*time.Second
		if good[1] != want || (!want && bad[1] == "") {
			t.Fatalf("offset %s: %v %v", offset, good, bad)
		}
	}
	next := p
	next.Bindings = append([]Binding(nil), p.Bindings...)
	fresh := *pin
	fresh.SetDNSLifetime(now.Add(30*time.Second), 60)
	next.Bindings[0].Applied.SOCKS5 = &fresh
	if !p.SamePaths(next) || !pin.ResolvedAt.Equal(now) {
		t.Fatal("timing-only comparison changed the path or mutated the old plan")
	}
	fresh.Address = "192.0.2.3"
	if p.SamePaths(next) {
		t.Fatal("changed endpoint treated as lifetime-only renewal")
	}
	fresh.Address = pin.Address
	next.Bindings[0].Fingerprint = strings.Repeat("a", 64)
	if p.SamePaths(next) {
		t.Fatal("changed credential treated as lifetime-only renewal")
	}
}

func TestSOCKS5GuardRestrictsEveryOriginalMarkedPacket(t *testing.T) {
	p, snapshot := fixture(t)
	p.Bindings[0].Applied.SOCKS5 = &networkconfig.ResolvedSOCKS5{Address: "192.0.2.2", Port: 1080, UDP: true}
	rules, err := p.Rules()
	if err != nil {
		t.Fatal(err)
	}
	match := "meta mark 0x43000001 ct direction original "
	for _, want := range []string{match + "ip daddr 192.0.2.2 tcp dport 1080 return", match + "ip daddr 192.0.2.2 udp dport 1-65535 return", match + "drop"} {
		if !strings.Contains(rules, want) {
			t.Fatal("missing transport fence", want)
		}
	}
	if strings.Contains(rules, " accept\n") || strings.Contains(rules, "ct state established") || strings.Index(rules, "meta oif != 2 drop") > strings.Index(rules, "tcp dport 1080 return") {
		t.Fatal("transport bypassed interface/general policy or exempted established traffic")
	}
	good, bad := p.Healthy(snapshot, snapshot.SampledAt)
	if !good[1] || len(bad) != 0 {
		t.Fatal("healthy pinned endpoint could not renew", bad)
	}
	snapshot.Interfaces[0].Addresses = append(snapshot.Interfaces[0].Addresses, "192.0.2.2/24")
	good, bad = p.Healthy(snapshot, snapshot.SampledAt)
	if good[1] || bad[1] == "" {
		t.Fatal("endpoint becoming local retained its lease")
	}
	p.Bindings[0].Applied.SOCKS5.UDP = false
	rules, err = p.Rules()
	if err != nil || strings.Contains(rules, "udp dport 1-65535 return") {
		t.Fatal("TCP-only transport retained UDP permission", err)
	}
	p.Bindings[0].Applied.SOCKS5.Address = "10.0.0.2"
	if _, err = p.Rules(); err == nil {
		t.Fatal("private upstream obtained permission without local authorization")
	}
}

func TestPrivateTransportRulesUseOnlyAppliedEndpointAndGrantedUDPPorts(t *testing.T) {
	p, snapshot := fixture(t)
	tcp := networkconfig.TransportGrant{NodeID: 1, EgressProfileID: 1, Purpose: "socks5", Network: "tcp", Address: "10.20.0.0/24", Port: 1080}
	udp := tcp
	udp.Network, udp.Port, udp.PortEnd = "udp", 20000, 20100
	pin, err := (networkconfig.ResolvedSOCKS5{Address: "10.20.0.5", Port: 1080, UDP: true}).WithTransportGrants(1, 1, []networkconfig.TransportGrant{tcp, udp})
	if err != nil {
		t.Fatal(err)
	}
	p.Bindings[0].Applied.SOCKS5 = &pin
	rules, err := p.Rules()
	if err != nil || !strings.Contains(rules, "ip daddr 10.20.0.5 udp dport 20000-20100 return") || strings.Contains(rules, "udp dport 1-65535") {
		t.Fatal("binding guard widened private relay permission", err)
	}
	exceptions, err := p.TransportRules()
	if err != nil || !strings.Contains(exceptions, "meta mark 0x43000001 ct direction original ip daddr 10.20.0.5 tcp dport 1080 accept") || !strings.Contains(exceptions, "udp dport 20000-20100 accept") || strings.Contains(exceptions, "/24") || strings.Contains(exceptions, "ct state established") {
		t.Fatal("exception did not retain exact applied identity", err)
	}
	if strings.Index(exceptions, "fib daddr type local drop") > strings.Index(exceptions, "tcp dport 1080 accept") {
		t.Fatal("local destination could use a transport exception")
	}
	snapshot.Interfaces[0].Addresses = append(snapshot.Interfaces[0].Addresses, "10.20.0.5/24")
	good, bad := p.Healthy(snapshot, snapshot.SampledAt)
	if good[1] || bad[1] == "" {
		t.Fatal("a root grant bypassed local-address rejection")
	}
	p.Bindings[0].Wanted.EgressProfileID = 2
	if p.Validate() == nil {
		t.Fatal("another profile reused applied authority")
	}
	p.Bindings[0].Wanted.EgressProfileID = 1
	p.Bindings[0].Pending = true
	p.Bindings[0].Applied = networkconfig.Resolved{}
	exceptions, err = p.TransportRules()
	if err != nil || strings.Contains(exceptions, " accept") {
		t.Fatal("pending plan enabled private access", err)
	}
}

func TestPendingBindingCannotRenewEvenWithForgedReadiness(t *testing.T) {
	p, snapshot := fixture(t)
	p.Bindings[0].Pending = true
	p.Bindings[0].Applied = networkconfig.Resolved{}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	ready, bad := p.Healthy(snapshot, time.Now())
	if ready[1] || bad[1] == "" {
		t.Fatal("pending application became healthy")
	}
	rules, err := p.Renew(map[int64]bool{1: true})
	if err != nil || strings.Contains(rules, "add element") {
		t.Fatal("pending application received a lease", err, rules)
	}
	rules, err = p.Rules()
	if err != nil || !strings.Contains(rules, "meta mark 0x43000001") || !strings.Contains(rules, "th dport 24443") {
		t.Fatal("pending application is not fenced", err, rules)
	}
}

func TestMonitorDiscardsSnapshotChangedDuringCollection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan netinventory.Change, 2)
	collected, renewed, revoked := 0, 0, 0
	err := (Monitor{
		Watch: func(context.Context) (<-chan netinventory.Change, error) { return events, nil },
		Collect: func() *agentproto.NetworkSnapshot {
			collected++
			events <- netinventory.Change{Indices: map[int]bool{2: true}}
			return &agentproto.NetworkSnapshot{}
		},
		Renew: func(context.Context, *agentproto.NetworkSnapshot) error { renewed++; return nil },
		Revoke: func(_ context.Context, _ map[int]bool, lost bool) error {
			if !lost {
				revoked++
				cancel()
			}
			return nil
		},
	}).Run(ctx)
	if err == nil || collected != 1 || renewed != 0 || revoked != 1 {
		t.Fatalf("collect=%d renew=%d revoke=%d err=%v", collected, renewed, revoked, err)
	}
}

func TestMonitorStopsRenewalOnClosedEventStream(t *testing.T) {
	events := make(chan netinventory.Change)
	close(events)
	renewed, revoked := 0, false
	err := (Monitor{Watch: func(context.Context) (<-chan netinventory.Change, error) { return events, nil }, Collect: func() *agentproto.NetworkSnapshot { return nil }, Renew: func(context.Context, *agentproto.NetworkSnapshot) error { renewed++; return nil }, Revoke: func(_ context.Context, _ map[int]bool, lost bool) error { revoked = lost; return nil }}).Run(context.Background())
	if err == nil || renewed != 0 || !revoked {
		t.Fatal("closed event stream did not fail closed")
	}
}

func TestMonitorRevokesOnSubscriptionFailure(t *testing.T) {
	for _, fail := range []bool{true, false} {
		revoked := false
		err := (Monitor{
			Watch: func(context.Context) (<-chan netinventory.Change, error) {
				if fail {
					return nil, errors.New("fixture subscription failed")
				}
				return nil, nil
			},
			Collect: func() *agentproto.NetworkSnapshot { t.Fatal("collected without an event subscription"); return nil },
			Renew: func(context.Context, *agentproto.NetworkSnapshot) error {
				t.Fatal("renewed without an event subscription")
				return nil
			},
			Revoke: func(_ context.Context, _ map[int]bool, lost bool) error { revoked = lost; return nil },
		}).Run(context.Background())
		if err == nil || !revoked {
			t.Fatal("subscription failure retained leases")
		}
	}
}
