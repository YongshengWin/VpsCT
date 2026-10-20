package nft

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
	"strings"
	"testing"
)

func TestSSTCPOnlyFirewallAndTranslation(t *testing.T) {
	nodes := []agentproto.NodeSpec{{Protocol: "ss", ListenPort: 30001, Params: map[string]any{"network": "tcp"}}}
	cfg := natFixtureConfig(natconfig.Mapping{PublicPort: 30001, LocalPort: 10001, Network: "tcp"})
	nat, err := NATRules(cfg, nodes, nil)
	if err != nil || !strings.Contains(nat, "tcp dport 10001 dnat to :30001") || strings.Contains(nat, "udp dport") {
		t.Fatalf("TCP translation: %v %s", err, nat)
	}
	ingress, err := IngressRules(ingressSnapshot(), nodes)
	if err != nil || !strings.Contains(ingress, "tcp dport") || strings.Contains(ingress, "udp dport") {
		t.Fatalf("TCP firewall: %v %s", err, ingress)
	}
}
