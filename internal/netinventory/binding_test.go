package netinventory

import (
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

func bindingFixture() (networkconfig.Node, networkconfig.Direct, *agentproto.NetworkSnapshot) {
	id := "11111111111111111111111111111111"
	n := networkconfig.Node{ListenMode: "address", ListenAddress: "192.0.2.1", ListenInterfaceID: id, AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}
	d := networkconfig.Direct{InterfaceID: id, SourceIPv4: &networkconfig.Address{InterfaceID: id, Address: "192.0.2.1"}, Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}}
	s := &agentproto.NetworkSnapshot{Version: 1, CollectorID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BootID: "boot", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "wan1", Index: 2, Kind: "ether", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24", "2001:db8::1/64"}}}}
	s.Interfaces[0].UsableAddresses = append([]string(nil), s.Interfaces[0].Addresses...)
	return n, d, s
}

func TestBindingResolvesStableIdentityAndProtectsValidatedCandidate(t *testing.T) {
	n, d, s := bindingFixture()
	s.Interfaces[0].Name = "renamed0"
	r, err := ResolveBinding(n, &d, s, false, s.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if r.Direct.Interface.Name != "renamed0" || r.ListenInterface.Name != "renamed0" || len(r.Direct.Owners) != 1 {
		t.Fatalf("binding: %+v", r)
	}
	d.SourceIPv4.Address = "192.0.2.99"
	s.Interfaces[0].Name = "changed-again"
	if r.Direct.Config.SourceIPv4.Address != "192.0.2.1" || r.Direct.Interface.Name != "renamed0" {
		t.Fatal("input mutation changed a validated candidate")
	}
	n, d, s = bindingFixture()
	d.InterfaceID = ""
	r, err = ResolveBinding(n, &d, s, false, s.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if r.Direct.Interface != nil || len(r.Direct.Owners) != 1 {
		t.Fatal("source-only binding must retain an owner without selecting an egress interface")
	}
}

func TestControllerPreviewNeverMakesAnOldSampleLocallyApplicable(t *testing.T) {
	n, d, s := bindingFixture()
	now := s.SampledAt
	s.SampledAt = now.Add(-time.Minute)
	if err := ValidateBindingSelection(n, &d, s, false); err != nil {
		t.Fatal("controller preview cannot validate a complete reported selection", err)
	}
	if _, err := ResolveBinding(n, &d, s, false, now); err == nil {
		t.Fatal("preview weakened the local freshness requirement")
	}
	if !s.SampledAt.Equal(now.Add(-time.Minute)) {
		t.Fatal("preview falsified observation freshness")
	}
	s.Interfaces[0].UsableAddresses = nil
	if err := ValidateBindingSelection(n, &d, s, false); err == nil {
		t.Fatal("preview accepted an unusable address")
	}
}

func TestBindingFailsClosedAcrossIdentityAddressAndSnapshotChanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*networkconfig.Node, *networkconfig.Direct, *agentproto.NetworkSnapshot)
	}{
		{"same-name replacement", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			s.Interfaces[0].ID = "22222222222222222222222222222222"
		}},
		{"down", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			s.Interfaces[0].Up = false
		}},
		{"no carrier", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			s.Interfaces[0].Carrier = false
		}},
		{"address lost", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			s.Interfaces[0].Addresses = nil
			s.Interfaces[0].UsableAddresses = nil
		}},
		{"address not usable", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			s.Interfaces[0].UsableAddresses = nil
		}},
		{"address moved", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			other := s.Interfaces[0]
			other.ID, other.Index, other.Name = "22222222222222222222222222222222", 3, "wan2"
			s.Interfaces[0].Addresses = nil
			s.Interfaces[0].UsableAddresses = nil
			s.Interfaces = append(s.Interfaces, other)
		}},
		{"ambiguous address", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			other := s.Interfaces[0]
			other.ID, other.Index, other.Name = "22222222222222222222222222222222", 3, "wan2"
			s.Interfaces = append(s.Interfaces, other)
		}},
		{"incomplete", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			s.Status = "incomplete"
		}},
		{"stale", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			s.SampledAt = s.SampledAt.Add(-6 * time.Second)
		}},
		{"future", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			s.SampledAt = s.SampledAt.Add(2 * time.Second)
		}},
		{"reference missing", func(n *networkconfig.Node, d *networkconfig.Direct, s *agentproto.NetworkSnapshot) {
			n.EgressProfileID, n.EgressRevision = 0, 0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, d, s := bindingFixture()
			now := s.SampledAt
			tc.edit(&n, &d, s)
			if _, err := ResolveBinding(n, &d, s, false, now); err == nil {
				t.Fatal("unsafe binding accepted")
			}
		})
	}
}

func TestBindingRespectsServerIPv4Policy(t *testing.T) {
	n, d, s := bindingFixture()
	r, err := ResolveBinding(n, &d, s, true, s.SampledAt)
	if err != nil || r.Direct.Config.Family != "ipv4" {
		t.Fatalf("family must be narrowed: %+v, %v", r, err)
	}
	n.ListenAddress = "2001:db8::1"
	if _, err := ResolveBinding(n, &d, s, true, s.SampledAt); err == nil {
		t.Fatal("IPv6 listener accepted on IPv4-only server")
	}
	n, d, s = bindingFixture()
	d.DNS.Address = "2001:db8::53"
	if _, err := ResolveBinding(n, &d, s, true, s.SampledAt); err == nil {
		t.Fatal("IPv6 resolver accepted on IPv4-only server")
	}
}
