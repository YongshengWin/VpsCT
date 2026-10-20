package agent

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"strings"
	"testing"
	"time"
)

func TestRealityHandshakeUsesOuterResolverAndRejectsPrivateAnswers(t *testing.T) {
	dns := networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}
	snapshot := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "fixture", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: strings.Repeat("1", 32), Generation: strings.Repeat("b", 32), Name: "wan0", Index: 2, Kind: "ether", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}
	cfg := networkconfig.SOCKS5{Server: "198.51.100.2", ServerPort: 1080, Authentication: "none", Family: "ipv4", DNS: dns, Outer: networkconfig.Direct{Family: "ipv4", DNS: dns}, ConnectTimeoutSeconds: 10}
	policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 7, EgressRevision: 1}
	n := agentproto.NodeSpec{NodeID: 1, Protocol: "vless", Core: "singbox", Params: map[string]any{"handshake_server": "Handshake.Example.test.", "handshake_port": float64(8443)}, Network: &agentproto.NodeNetworkSpec{Policy: policy, SOCKS5: &agentproto.SOCKS5Egress{Config: cfg}}}
	answer := "203.0.113.25"
	r := &bindingRuntime{collect: func() *agentproto.NetworkSnapshot { return snapshot }}
	r.resolveSOCKS = func(_ context.Context, id int64, p networkconfig.Node, c networkconfig.SOCKS5, s *agentproto.NetworkSnapshot, v4 bool, now time.Time, g []networkconfig.TransportGrant) (networkconfig.Resolved, error) {
		if id != n.NodeID || p.EgressProfileID != 7 || c.Server != "Handshake.Example.test." || c.ServerPort != 8443 || c.Outer.DNS != dns {
			t.Fatal("camouflage DNS escaped its selected bootstrap resolver")
		}
		resolved, err := netinventory.ResolveBinding(p, &c.Outer, s, v4, now)
		resolved.SOCKS5 = &networkconfig.ResolvedSOCKS5{Address: answer, Port: c.ServerPort}
		return resolved, err
	}
	pin, err := r.resolveRealityHandshake(context.Background(), n, snapshot, true)
	if err != nil || pin.Address != answer || pin.Host != "handshake.example.test" {
		t.Fatal(pin, err)
	}
	for _, bad := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.1", "192.0.2.1"} {
		answer = bad
		if _, err := r.resolveRealityHandshake(context.Background(), n, snapshot, true); err == nil {
			t.Fatal("unsafe handshake pin accepted", bad)
		}
	}
}
