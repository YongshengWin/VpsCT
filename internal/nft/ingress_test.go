package nft

import (
	"crypto/sha256"
	"ctlvps/internal/agentproto"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func ingressSnapshot(extra ...string) []byte {
	return []byte(`{"nftables":[{"chain":{"family":"inet","table":"filter","name":"input","hook":"input","policy":"drop"}}` + strings.Join(extra, "") + `, {"rule":{"family":"inet","table":"filter","chain":"input","handle":1,"expr":[{"drop":null}]}}]}`)
}
func TestIngressLifecycle(t *testing.T) {
	nodes := []agentproto.NodeSpec{{Protocol: "anytls", ListenPort: 47399}, {Protocol: "vless", ListenPort: 22228}, {Protocol: "hysteria2", ListenPort: 22229}, {Protocol: "ss", ListenPort: 22230}, {Protocol: "snell", ListenPort: 22231, Blocked: true}}
	rules, err := IngressRules(ingressSnapshot(), nodes)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"insert rule inet filter input tcp dport { 22228, 22230, 47399 }", "udp dport { 22229, 22230 }"} {
		if !strings.Contains(rules, want) {
			t.Fatal(rules)
		}
	}
	if strings.Contains(rules, "22231") || strings.Contains(rules, "flush") || strings.Contains(rules, "handle 1\n") {
		t.Fatal(rules)
	}
	old := fmt.Sprintf(`,{"rule":{"family":"inet","table":"filter","chain":"input","handle":9,"comment":"ctlvps-node-ingress:tcp:%x"}}`, sha256.Sum256([]byte("47399")))
	single := []agentproto.NodeSpec{{Protocol: "anytls", ListenPort: 47399}}
	if r, e := IngressRules(ingressSnapshot(old), single); e != nil || r != "" {
		t.Fatalf("not idempotent: %s %v", r, e)
	}
	// Rules must stay before administrator drop rules, and duplicate tags
	// must not masquerade as a complete TCP+UDP pair.
	misplaced := strings.TrimSuffix(string(ingressSnapshot()), `]}`) + old + `]}`
	if r, e := IngressRules([]byte(misplaced), single); e != nil || r == "" {
		t.Fatalf("misplaced allowance not repaired: %s %v", r, e)
	}
	if r, e := IngressRules(ingressSnapshot(old, old), []agentproto.NodeSpec{{Protocol: "ss", ListenPort: 47399}}); e != nil || !strings.Contains(r, "udp dport") {
		t.Fatalf("duplicate allowance not repaired: %s %v", r, e)
	}
	for _, ns := range [][]agentproto.NodeSpec{nil, {{Protocol: "anytls", ListenPort: 47400}}, {{Protocol: "anytls", ListenPort: 47399, Blocked: true}}} {
		r, e := IngressRules(ingressSnapshot(old), ns)
		if e != nil || !strings.Contains(r, "delete rule inet filter input handle 9") {
			t.Fatalf("stale allowance: %s %v", r, e)
		}
	}
}
func TestIngressRejectsOtherManagersAndBadInputs(t *testing.T) {
	nodes := []agentproto.NodeSpec{{Protocol: "anytls", ListenPort: 12345}}
	for _, extra := range []string{`,{"chain":{"family":"inet","table":"firewalld","name":"filter_INPUT","hook":"input","policy":"drop"}}`, `,{"chain":{"family":"ip","table":"filter","name":"INPUT","hook":"input","policy":"accept"}},{"rule":{"family":"ip","table":"filter","chain":"INPUT","handle":3}}`} {
		if _, e := IngressRules(ingressSnapshot(extra), nodes); e == nil {
			t.Fatal("unsupported firewall accepted")
		}
	}
	if r, e := IngressRules([]byte(`{"nftables":[]}`), nodes); e != nil || r != "" {
		t.Fatal("permissive host changed")
	}
	for _, n := range []agentproto.NodeSpec{{Protocol: "anytls", ListenPort: 0}, {Protocol: "unknown", ListenPort: 12}} {
		if _, e := IngressRules(ingressSnapshot(), []agentproto.NodeSpec{n}); e == nil {
			t.Fatal("invalid node accepted")
		}
	}
	if _, e := IngressRules(json.RawMessage(`invalid`), nodes); e == nil {
		t.Fatal("invalid snapshot accepted")
	}
}
