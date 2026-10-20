package nft

import (
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/networkconfig"
)

func TestNATForwardPreservesAdmissionAndTransport(t *testing.T) {
	cfg := natFixtureConfig(natconfig.Mapping{PublicPort: 31001, LocalPort: 11001, Count: 4})
	forwards := []agentproto.ForwardSpec{
		{Config: networkconfig.Forward{ListenPort: 31001, Network: "tcp"}},
		{Config: networkconfig.Forward{ListenPort: 31002, Network: "udp"}},
		{Config: networkconfig.Forward{ListenPort: 31003, Network: "both"}},
		{Blocked: true, Config: networkconfig.Forward{ListenPort: 31004, Network: "both"}},
	}
	rules, err := NATRules(cfg, nil, forwards)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tcp dport 11001 dnat to :31001", "udp dport 11002 dnat to :31002", "tcp dport 11003 dnat to :31003", "udp dport 11003 dnat to :31003"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing forward translation %q: %s", want, rules)
		}
	}
	for _, bad := range []string{"udp dport 11001", "tcp dport 11002", "dport 11004", "ct mark", "meta mark", "counter", "ctlvps_forwards", "ctlvps_egress"} {
		if strings.Contains(rules, bad) {
			t.Fatalf("NAT changed admission/accounting or captured blocked/unsupported transport: %s", rules)
		}
	}
	forwards[0].Retired = true
	retired, err := NATRules(cfg, nil, forwards)
	if err != nil || strings.Contains(retired, "dport 11001") {
		t.Fatal("retired forward retained translation", retired, err)
	}
}

func TestNATForwardRejectsDuplicateAndUnknownListeners(t *testing.T) {
	cfg := natFixtureConfig(natconfig.Mapping{PublicPort: 31001, LocalPort: 11001})
	for _, forward := range []agentproto.ForwardSpec{
		{Config: networkconfig.Forward{ListenPort: 31001, Network: "other"}},
		{Config: networkconfig.Forward{ListenPort: 0, Network: "tcp"}},
	} {
		if _, err := NATRules(cfg, nil, []agentproto.ForwardSpec{forward}); err == nil {
			t.Fatal("invalid forward accepted", forward)
		}
	}
	forward := agentproto.ForwardSpec{Config: networkconfig.Forward{ListenPort: 31001, Network: "tcp"}}
	if _, err := NATRules(cfg, []agentproto.NodeSpec{{Protocol: "vless", ListenPort: 31001, Blocked: true}}, []agentproto.ForwardSpec{forward}); err == nil {
		t.Fatal("forward reused reserved node listener")
	}
	if _, err := NATRules(cfg, nil, []agentproto.ForwardSpec{forward, forward}); err == nil {
		t.Fatal("forward reused another forward listener")
	}
}
