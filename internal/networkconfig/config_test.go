package networkconfig

import "testing"

const testID = "11111111111111111111111111111111"

func TestNetworkPolicyRejectsAmbiguousOrFallbackConfiguration(t *testing.T) {
	base := Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}
	for _, tc := range []struct {
		name string
		edit func(*Node)
	}{
		{"fallback", func(n *Node) { n.OnUnavailable = "direct" }},
		{"missing revision", func(n *Node) { n.EgressProfileID = 1 }},
		{"orphan revision", func(n *Node) { n.EgressRevision = 1 }},
		{"all with address", func(n *Node) { n.ListenAddress = "192.0.2.1" }},
		{"address without owner", func(n *Node) { n.ListenMode, n.ListenAddress = "address", "192.0.2.1" }},
		{"wildcard address", func(n *Node) { n.ListenMode, n.ListenAddress, n.ListenInterfaceID = "address", "::", testID }},
		{"invalid address mode", func(n *Node) { n.AdvertiseMode = "guess" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := base
			tc.edit(&n)
			if n.Validate() == nil {
				t.Fatal("accepted invalid network policy")
			}
		})
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	base.ListenMode, base.ListenAddress, base.ListenInterfaceID = "address", "2001:db8::1", testID
	base.EgressProfileID, base.EgressRevision = 1, 3
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectRequiresIndependentDNSAndSourceOwnership(t *testing.T) {
	base := Direct{Family: "dual", DNS: Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}
	for _, tc := range []struct {
		name string
		edit func(*Direct)
	}{
		{"system DNS fallback", func(d *Direct) { d.DNS.Transport = "local" }},
		{"recursive endpoint", func(d *Direct) { d.DNS.Address = "resolver.example" }},
		{"wrong DNS family", func(d *Direct) { d.Family = "ipv6" }},
		{"missing owner", func(d *Direct) { d.SourceIPv4 = &Address{Address: "192.0.2.1"} }},
		{"different owner", func(d *Direct) {
			d.InterfaceID = testID
			d.SourceIPv4 = &Address{InterfaceID: "22222222222222222222222222222222", Address: "192.0.2.1"}
		}},
		{"wrong source family", func(d *Direct) { d.SourceIPv4 = &Address{InterfaceID: testID, Address: "2001:db8::1"} }},
		{"mapped source", func(d *Direct) { d.SourceIPv4 = &Address{InterfaceID: testID, Address: "::ffff:192.0.2.1"} }},
		{"source outside family", func(d *Direct) {
			d.Family = "ipv4"
			d.SourceIPv6 = &Address{InterfaceID: testID, Address: "2001:db8::1"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := base
			tc.edit(&d)
			if d.Validate() == nil {
				t.Fatal("accepted invalid direct policy")
			}
		})
	}
	base.SourceIPv4 = &Address{InterfaceID: testID, Address: "192.0.2.1"}
	if err := base.Validate(); err != nil {
		t.Fatal("source-only binding must remain supported:", err)
	}
	for _, raw := range []string{"0.0.0.0", "::", "127.0.0.1", "::1", "fe80::1%wan0", "224.0.0.1", "192.0.2.1/24", "192.0.2.1\n"} {
		if _, err := HostAddress(raw); err == nil {
			t.Errorf("unsafe literal accepted: %q", raw)
		}
	}
}
