package nft

import (
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

func TestBootstrapRulesDoNotAuthorizeProxyOrUnappliedTransport(t *testing.T) {
	resolver := networkconfig.Resolver{Transport: "udp", Address: "10.23.0.53", Port: 15353}
	grants := []networkconfig.TransportGrant{
		{NodeID: 1, EgressProfileID: 2, Purpose: "bootstrap_dns", Network: "tcp", Address: resolver.Address, Port: resolver.Port},
		{NodeID: 1, EgressProfileID: 2, Purpose: "bootstrap_dns", Network: "udp", Address: resolver.Address, Port: resolver.Port},
		{NodeID: 1, EgressProfileID: 2, Purpose: "socks5", Network: "tcp", Address: "10.23.0.2", Port: 1080},
	}
	cfg := networkconfig.SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "none", Family: "dual", ConnectTimeoutSeconds: 10,
		DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}, Outer: networkconfig.Direct{Family: "dual", DNS: resolver}}
	n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", TransportGrants: grants, Network: &agentproto.NodeNetworkSpec{
		Policy: networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 2, EgressRevision: 1}, SOCKS5: &agentproto.SOCKS5Egress{Config: cfg}}}
	group := map[int64]string{1: "/ctlvps.slice/public"}
	rules, err := EgressRules([]agentproto.NodeSpec{n}, group, nil)
	if err != nil {
		t.Fatal(err)
	}
	uid := strings.Index(rules, "ctlvps_bootstrap meta skuid != 0 drop")
	for _, transport := range []string{"tcp", "udp"} {
		i := strings.Index(rules, "ctlvps_bootstrap meta mark 0x44000001 ip daddr 10.23.0.53 "+transport+" dport 15353 accept")
		if uid < 0 || i < uid {
			t.Fatal("bootstrap accepts before root socket ownership check")
		}
	}
	if strings.Contains(rules, "daddr 10.23.0.2") || strings.Contains(rules, "0x43000001 ip daddr") {
		t.Fatal("desired grant became proxy authority before an applied guard plan")
	}
	for _, edit := range []func(*agentproto.NodeSpec){
		func(n *agentproto.NodeSpec) { n.Blocked = true },
		func(n *agentproto.NodeSpec) { n.Retired = true },
		func(n *agentproto.NodeSpec) { n.TransportGrants = grants[:1] },
		func(n *agentproto.NodeSpec) { n.TransportGrants = grants[1:] },
		func(n *agentproto.NodeSpec) { n.NodeID = 3 },
	} {
		copy := n
		edit(&copy)
		rules, err := EgressRules([]agentproto.NodeSpec{copy}, map[int64]string{copy.NodeID: group[1]}, nil)
		if err != nil || strings.Contains(rules, " dport 15353 accept") {
			t.Fatal("missing/inactive/wrong-node grants still authorize private DNS", err)
		}
	}
}
