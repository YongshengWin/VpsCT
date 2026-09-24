package netinventory

import (
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

func TestSOCKS5EndpointIsPinnedAndPrivateAuthorizationStaysSeparate(t *testing.T) {
	id := strings.Repeat("1", 32)
	s := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "fixture", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: strings.Repeat("b", 32), Name: "wan1", Index: 2, Kind: "ether", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}
	n := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}
	cfg := networkconfig.SOCKS5{Server: "192.0.2.2", ServerPort: 1080, Authentication: "none", UDP: true, Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.53", Port: 53}, Outer: networkconfig.Direct{InterfaceID: id, Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}}, ConnectTimeoutSeconds: 10}
	r, err := ResolveSOCKS5(n, cfg, s, true, time.Now())
	if err != nil || r.SOCKS5 == nil || r.SOCKS5.Address != cfg.Server || !r.SOCKS5.UDP || r.Direct.Config.Family != "ipv4" || cfg.Outer.Family != "dual" {
		t.Fatal("literal transport lost its immutable pin or effective family", err)
	}
	for _, address := range []string{"upstream.example.test", "192.0.2.1", "10.0.0.2", "100.64.0.2", "169.254.169.254", "127.0.0.1", "fd00::2", "64:ff9b::a00:2", "2002:a00:2::1", "::ffff:192.0.2.2"} {
		candidate := cfg
		candidate.Server = address
		if _, err := ResolveSOCKS5(n, candidate, s, false, time.Now()); err == nil {
			t.Fatal("unapproved or unresolved endpoint accepted", address)
		}
	}
	cfg.Server, cfg.Family = "2001:db8:1::2", "ipv6"
	cfg.DNS.Address = "2001:db8:ffff::53"
	if _, err := ResolveSOCKS5(n, cfg, s, true, time.Now()); err == nil {
		t.Fatal("IPv4-only server allowed explicitly IPv6 business")
	}
	if _, err := ResolveSOCKS5(n, cfg, s, false, time.Now()); err != nil {
		t.Fatal("IPv6 transport rejected", err)
	}
}
