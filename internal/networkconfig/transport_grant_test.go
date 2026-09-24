package networkconfig

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestTransportGrantKeepsBindingPurposeAddressAndPortIndependent(t *testing.T) {
	g := TransportGrant{NodeID: 7, EgressProfileID: 3, Purpose: "socks5", Network: "tcp", Address: "10.20.0.0/24", Port: 1080}
	if err := ValidateTransportGrants([]TransportGrant{g}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		node, profile             int64
		purpose, network, address string
		port                      int
		want                      bool
	}{
		{7, 3, "socks5", "tcp", "10.20.0.5", 1080, true},
		{8, 3, "socks5", "tcp", "10.20.0.5", 1080, false},
		{7, 4, "socks5", "tcp", "10.20.0.5", 1080, false},
		{7, 3, "bootstrap_dns", "tcp", "10.20.0.5", 1080, false},
		{7, 3, "socks5", "udp", "10.20.0.5", 1080, false},
		{7, 3, "socks5", "tcp", "10.20.1.5", 1080, false},
		{7, 3, "socks5", "tcp", "10.20.0.5", 22, false},
	} {
		if got := AuthorizeTransport([]TransportGrant{g}, tc.node, tc.profile, tc.purpose, tc.network, netip.MustParseAddr(tc.address), tc.port); got != tc.want {
			t.Fatalf("authorization escaped its scope: %+v", tc)
		}
	}
	udp := g
	udp.Network, udp.Port, udp.PortEnd = "udp", 20000, 20100
	for _, port := range []int{19999, 20000, 20100, 20101} {
		if got := AuthorizeTransport([]TransportGrant{udp}, 7, 3, "socks5", "udp", netip.MustParseAddr("10.20.0.5"), port); got != (port >= 20000 && port <= 20100) {
			t.Fatal("UDP range", port)
		}
	}
}

func TestTransportGrantRejectsSpecialAddressesAndUnboundedOrAmbiguousInput(t *testing.T) {
	base := TransportGrant{NodeID: 7, EgressProfileID: 3, Purpose: "socks5", Network: "tcp", Address: "10.0.0.1", Port: 1080}
	for _, address := range []string{"0.0.0.0/0", "127.0.0.1", "169.254.169.254", "224.0.0.1", "240.0.0.1", "::", "::1", "fe80::1", "ff02::1", "64:ff9b::a00:1", "2002:a00:1::", "::ffff:10.0.0.1", "10.0.0.1/24", "upstream.example", "10.0.0.01", "10.0.0.1/32 ", "10.0.0.0/7"} {
		g := base
		g.Address = address
		if g.Validate() == nil {
			t.Fatal("accepted unsafe or ambiguous grant", address)
		}
	}
	for _, address := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.2.1", "100.64.0.0/10", "fd00::/64", "fd00::1"} {
		g := base
		g.Address = address
		if err := g.Validate(); err != nil {
			t.Fatal(address, err)
		}
	}
	for _, edit := range []func(*TransportGrant){func(g *TransportGrant) { g.NodeID = 0 }, func(g *TransportGrant) { g.EgressProfileID = 0 }, func(g *TransportGrant) { g.Purpose = "direct" }, func(g *TransportGrant) { g.Network = "all" }, func(g *TransportGrant) { g.Port = 0 }, func(g *TransportGrant) { g.PortEnd = 65536 }, func(g *TransportGrant) { g.PortEnd = g.Port }, func(g *TransportGrant) { g.Purpose = "bootstrap_dns"; g.PortEnd = 1081 }} {
		g := base
		edit(&g)
		if g.Validate() == nil {
			t.Fatal("accepted invalid grant", g)
		}
	}
	if ValidateTransportGrants([]TransportGrant{base, base}) == nil {
		t.Fatal("duplicate grant accepted")
	}
	grants := []TransportGrant{}
	for i := 0; i <= MaxBindingTransportGrants; i++ {
		g := base
		g.Port += i
		grants = append(grants, g)
	}
	if ValidateTransportGrants(grants) == nil {
		t.Fatal("per-binding grant limit absent")
	}
	if ValidateTransportGrants(make([]TransportGrant, MaxTransportGrants+1)) == nil {
		t.Fatal("total grant limit absent")
	}
}

func TestTransportGrantSelectionIsCanonicalAndDoesNotAliasPolicy(t *testing.T) {
	a := TransportGrant{NodeID: 7, EgressProfileID: 3, Purpose: "socks5", Network: "tcp", Address: "10.0.0.1", Port: 1080}
	b := a
	b.Network = "udp"
	b.PortEnd = 1090
	foreign := a
	foreign.NodeID = 8
	policy := []TransportGrant{b, foreign, a}
	want := []TransportGrant{a, b}
	if got := TransportGrantsFor(policy, 7, 3); !reflect.DeepEqual(got, want) {
		t.Fatal("noncanonical selection", got)
	}
	selected := TransportGrantsFor(policy, 7, 3)
	selected[0].Port = 22
	if !reflect.DeepEqual(policy, []TransportGrant{b, foreign, a}) {
		t.Fatal("mutated root policy")
	}
}

func TestPrivateSOCKS5ProofRequiresBothTransportsAndCurrentRootAuthority(t *testing.T) {
	tcp := TransportGrant{NodeID: 7, EgressProfileID: 3, Purpose: "socks5", Network: "tcp", Address: "10.20.0.0/24", Port: 1080}
	udp := tcp
	udp.Network, udp.Port, udp.PortEnd = "udp", 20000, 20100
	pin := ResolvedSOCKS5{Address: "10.20.0.5", Port: 1080, UDP: true}
	if _, err := pin.WithTransportGrants(7, 3, []TransportGrant{tcp}); err == nil {
		t.Fatal("TCP permission implied UDP")
	}
	if _, err := pin.WithTransportGrants(7, 3, []TransportGrant{udp}); err == nil {
		t.Fatal("UDP permission implied control TCP")
	}
	for _, ids := range [][2]int64{{8, 3}, {7, 4}} {
		if _, err := pin.WithTransportGrants(ids[0], ids[1], []TransportGrant{tcp, udp}); err == nil {
			t.Fatal("foreign binding borrowed authority")
		}
	}
	dns := tcp
	dns.Purpose = "bootstrap_dns"
	if _, err := pin.WithTransportGrants(7, 3, []TransportGrant{dns, udp}); err == nil {
		t.Fatal("DNS permission implied SOCKS")
	}
	approved, err := pin.WithTransportGrants(7, 3, []TransportGrant{udp, tcp})
	if err != nil {
		t.Fatal(err)
	}
	if approved.ValidateForBinding(8, 3) == nil || approved.ValidateForBinding(7, 4) == nil {
		t.Fatal("persisted proof escaped its binding")
	}
	if !approved.StillAuthorized([]TransportGrant{tcp, udp}) || approved.StillAuthorized([]TransportGrant{tcp}) || approved.StillAuthorized(nil) {
		t.Fatal("revoked authority kept old endpoint valid")
	}
	if got := approved.UDPPorts(); !reflect.DeepEqual(got, []TransportPortRange{{20000, 20100}}) {
		t.Fatal("private UDP ports widened", got)
	}
	udpMore := udp
	udpMore.Port, udpMore.PortEnd = 20050, 20200
	approved, err = pin.WithTransportGrants(7, 3, []TransportGrant{tcp, udp, udpMore})
	if err != nil || !reflect.DeepEqual(approved.UDPPorts(), []TransportPortRange{{20000, 20200}}) {
		t.Fatal("overlap merge changed permission", err)
	}
	public := ResolvedSOCKS5{Address: "192.0.2.5", Port: 1080, UDP: true}
	if got, err := public.WithTransportGrants(7, 3, []TransportGrant{tcp, udp}); err != nil || len(got.TransportGrants) != 0 || !reflect.DeepEqual(got.UDPPorts(), []TransportPortRange{{1, 65535}}) {
		t.Fatal("public baseline changed", err)
	}
	approved.Address = "10.21.0.5"
	if approved.Validate() == nil {
		t.Fatal("proof reused after DNS changed outside its grant")
	}
}
