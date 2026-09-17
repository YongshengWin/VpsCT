package nft

import (
	"context"
	"crypto/sha256"
	"ctlvps/internal/agentproto"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func TestCompatibilitySSHProtectionDoesNotBlockNodePorts(t *testing.T) {
	chain := &ingressEntry{Family: "ip", Table: "filter", Name: "INPUT", Policy: "accept"}
	var rule ingressEntry
	if err := json.Unmarshal([]byte(`{"expr":[{"match":{"op":"==","left":{"meta":{"key":"l4proto"}},"right":"tcp"}},{"xt":{"type":"match","name":"multiport"}},{"counter":{"packets":1,"bytes":1}},{"jump":{"target":"f2b-sshd"}}]}`), &rule); err != nil {
		t.Fatal(err)
	}
	listing := "-P INPUT ACCEPT\n-A INPUT -p tcp -m multiport --dports 22 -j f2b-sshd\n"
	ports := map[string]map[int]bool{"tcp": {443: true}, "udp": {22: true}}
	if !compatInputDisjoint(chain, []*ingressEntry{&rule}, ports, listing) {
		t.Fatal("unrelated SSH rule rejected")
	}
	for _, bad := range []string{"", strings.Replace(listing, "--dports 22", "--dports 22,443", 1), strings.Replace(listing, "--dports 22", "--dports 22:443", 1), strings.Replace(listing, "f2b-sshd", "another-chain", 1), strings.Replace(listing, "ACCEPT", "DROP", 1), listing + "-A INPUT -j DROP\n", strings.Replace(listing, "--dports 22", "--dports invalid", 1)} {
		if compatInputDisjoint(chain, []*ingressEntry{&rule}, ports, bad) {
			t.Fatalf("unsafe listing accepted: %q", bad)
		}
	}
	extra := `,{"chain":{"family":"ip","table":"filter","name":"INPUT","hook":"input","policy":"accept"}},{"rule":{"family":"ip","table":"filter","chain":"INPUT","expr":[{"match":{"op":"==","left":{"meta":{"key":"l4proto"}},"right":"tcp"}},{"xt":{"type":"match","name":"multiport"}},{"counter":{}},{"jump":{"target":"f2b-sshd"}}]}},{"chain":{"family":"ip","table":"filter","name":"f2b-sshd"}},{"rule":{"family":"ip","table":"filter","chain":"f2b-sshd","expr":[{"reject":null}]}}`
	nodes := []agentproto.NodeSpec{{Protocol: "vless", ListenPort: 443}}
	if _, err := IngressRules(ingressSnapshot(extra), nodes); err == nil {
		t.Fatal("opaque xt rule accepted without proof")
	}
	got, err := ingressRules(ingressSnapshot(extra), nodes, map[string]string{"ip": listing})
	if err != nil || !strings.Contains(got, "tcp dport { 443 }") || strings.Contains(got, "f2b") || strings.Contains(got, "rule ip filter") {
		t.Fatalf("unexpected changes %q %v", got, err)
	}
	nodes[0].ListenPort = 22
	if _, err := ingressRules(ingressSnapshot(extra), nodes, map[string]string{"ip": listing}); err == nil {
		t.Fatal("overlapping SSH protection ignored")
	}
	rule.Expr = append(rule.Expr, map[string]json.RawMessage{"drop": json.RawMessage(`null`)})
	if compatInputDisjoint(chain, []*ingressEntry{&rule}, ports, listing) {
		t.Fatal("nft extra action ignored")
	}
}

func TestEnsureIngressUsesOnlyNftCompatibilityBackend(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	snapshot := `{"nftables":[{"chain":{"family":"ip","table":"filter","name":"INPUT","hook":"input","policy":"accept"}},{"rule":{"family":"ip","table":"filter","chain":"INPUT","expr":[{"match":{"op":"==","left":{"meta":{"key":"l4proto"}},"right":"tcp"}},{"xt":{"type":"match","name":"multiport"}},{"counter":{}},{"jump":{"target":"f2b-sshd"}}]}}]}`
	nftBin := filepath.Join(dir, "nft")
	if e := os.WriteFile(nftBin, []byte("#!/bin/sh\necho '"+snapshot+"'\n"), 0700); e != nil {
		t.Fatal(e)
	}
	m := &Manager{Bin: nftBin}
	for _, backend := range []string{"nf_tables", "legacy"} {
		script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'iptables (" + backend + ")'; else echo '-P INPUT ACCEPT'; echo '-A INPUT -p tcp -m multiport --dports 22 -j f2b-sshd'; fi\n"
		if e := os.WriteFile(filepath.Join(dir, "iptables"), []byte(script), 0700); e != nil {
			t.Fatal(e)
		}
		err := m.EnsureIngress(context.Background(), []agentproto.NodeSpec{{Protocol: "vless", ListenPort: 443}})
		if (err == nil) != (backend == "nf_tables") {
			t.Fatalf("backend %s: %v", backend, err)
		}
	}
}
