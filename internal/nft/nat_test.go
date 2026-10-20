package nft

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/networkconfig"
)

func natFixtureConfig(mappings ...natconfig.Mapping) natconfig.Config {
	return natconfig.Config{Schema: 1, Mappings: mappings}
}

func TestNATRulesOnlyTranslateAllocatedNodeProtocols(t *testing.T) {
	nodes := []agentproto.NodeSpec{
		{Protocol: "vless", ListenPort: 30001},
		{Protocol: "ss", ListenPort: 30002},
		{Protocol: "hysteria2", ListenPort: 30003},
		{Protocol: "mieru", ListenPort: 30004, Params: map[string]any{"transport": "UDP"}},
		{Protocol: "snell", ListenPort: 30005, Blocked: true},
		{Protocol: "tuic", ListenPort: 30006, Retired: true},
	}
	cfg := natFixtureConfig(natconfig.Mapping{PublicPort: 30001, LocalPort: 10001, Count: 7})
	cfg.Interface, cfg.LocalAddress = "eth0", "192.0.2.10"
	rules, err := NATRules(cfg, nodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type nat hook prerouting priority dstnat; policy accept;",
		"meta nfproto ipv4 iifname != \"lo\" fib daddr type local iifname \"eth0\" ip daddr 192.0.2.10",
		"tcp dport 10001 dnat to :30001",
		"tcp dport 10002 dnat to :30002",
		"udp dport 10002 dnat to :30002",
		"udp dport 10003 dnat to :30003",
		"udp dport 10004 dnat to :30004",
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing %q:\n%s", want, rules)
		}
	}
	for _, absent := range []string{"udp dport 10001", "tcp dport 10003", "tcp dport 10004", "dport 10005", "dport 10006", "dport 10007", "flush ruleset", "ctlvps_nodes", "ct mark", "counter"} {
		if strings.Contains(rules, absent) {
			t.Fatalf("unexpected %q:\n%s", absent, rules)
		}
	}
	if strings.Count(rules, "add rule ") != 5 {
		t.Fatal("NAT captured an unallocated or unsupported transport", rules)
	}
	// Desired-state ordering must not change the resulting transaction.
	nodes[0], nodes[3] = nodes[3], nodes[0]
	reordered, err := NATRules(cfg, nodes, nil)
	if err != nil || reordered != rules {
		t.Fatalf("unstable transaction:\n%s\n%v", reordered, err)
	}
}

func TestNATProtocolCoverageAndEmptyRules(t *testing.T) {
	for _, protocol := range []string{"vless", "vmess", "trojan", "anytls", "snell", "shadowsocks", "ss", "hysteria2", "tuic", "wireguard", "mieru"} {
		t.Run(protocol, func(t *testing.T) {
			n := agentproto.NodeSpec{Protocol: protocol, ListenPort: 31001, Params: map[string]any{"transport": "TCP"}}
			cfg := natFixtureConfig(natconfig.Mapping{PublicPort: 31001, LocalPort: 11001})
			rules, err := NATRules(cfg, []agentproto.NodeSpec{n}, nil)
			if err != nil || !strings.Contains(rules, "dnat to :31001") {
				t.Fatal(rules, err)
			}
		})
	}
	for _, cfg := range []natconfig.Config{
		natFixtureConfig(),
		natFixtureConfig(natconfig.Mapping{PublicPort: 31001, LocalPort: 31001}),
		natFixtureConfig(natconfig.Mapping{PublicPort: 31002, LocalPort: 11002}),
		natFixtureConfig(natconfig.Mapping{PublicPort: 31001, LocalPort: 11001, Network: "udp"}),
	} {
		rules, err := NATRules(cfg, []agentproto.NodeSpec{{Protocol: "vless", ListenPort: 31001}}, nil)
		if err != nil || rules != "" {
			t.Fatal("empty or identity mapping changed packets", rules, err)
		}
	}
	for _, n := range []agentproto.NodeSpec{
		{Protocol: "unknown", ListenPort: 31001},
		{Protocol: "mieru", ListenPort: 31001},
		{Protocol: "vless", ListenPort: 0},
	} {
		if _, err := NATRules(natFixtureConfig(natconfig.Mapping{PublicPort: 31001, LocalPort: 11001}), []agentproto.NodeSpec{n}, nil); err == nil {
			t.Fatal("invalid node accepted", n)
		}
	}
}

func TestNATRejectsOtherResourceListenerCollisions(t *testing.T) {
	cfg := natFixtureConfig(natconfig.Mapping{PublicPort: 32001, LocalPort: 12001})
	base := agentproto.NodeSpec{Protocol: "ss", ListenPort: 32001}
	for _, blocked := range []bool{false, true} {
		nodes := []agentproto.NodeSpec{base, {Protocol: "vless", ListenPort: 12001, Blocked: blocked}}
		if _, err := NATRules(cfg, nodes, nil); err == nil {
			t.Fatal("NAT hijacked a node listener")
		}
		forward := agentproto.ForwardSpec{Blocked: blocked, Config: networkconfig.Forward{ListenPort: 12001}}
		if _, err := NATRules(cfg, []agentproto.NodeSpec{base}, []agentproto.ForwardSpec{forward}); err == nil {
			t.Fatal("NAT hijacked a fixed forward listener")
		}
	}
	retiredNode := agentproto.NodeSpec{Protocol: "vless", ListenPort: 12001, Retired: true}
	retiredForward := agentproto.ForwardSpec{Retired: true, Config: networkconfig.Forward{ListenPort: 12001}}
	if rules, err := NATRules(cfg, []agentproto.NodeSpec{base, retiredNode}, []agentproto.ForwardSpec{retiredForward}); err != nil || rules == "" {
		t.Fatal("retired listener remained reserved", rules, err)
	}
	// Even an unused mapping must not be activated later over a live listener.
	cfg.Mappings[0].PublicPort = 32002
	if _, err := NATRules(cfg, []agentproto.NodeSpec{base, {Protocol: "snell", ListenPort: 12001}}, nil); err == nil {
		t.Fatal("unused conflicting mapping accepted")
	}
}

func natManagerFixture(t *testing.T, tables string) (*Manager, string, string) {
	t.Helper()
	dir := t.TempDir()
	calls, inputs := filepath.Join(dir, "calls"), filepath.Join(dir, "inputs")
	t.Setenv("CTLVPS_NAT_TEST_CALLS", calls)
	t.Setenv("CTLVPS_NAT_TEST_INPUTS", inputs)
	t.Setenv("CTLVPS_NAT_TEST_TABLES", tables)
	bin := filepath.Join(dir, "nft")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CTLVPS_NAT_TEST_CALLS"
case "$*" in
  '-j list tables')
    [ "${CTLVPS_NAT_TEST_LIST_FAIL:-0}" = 0 ] || exit 1
    printf '%s\n' "$CTLVPS_NAT_TEST_TABLES"
    ;;
  '--check -f -')
    /bin/cat >> "$CTLVPS_NAT_TEST_INPUTS"
    [ "${CTLVPS_NAT_TEST_CHECK_FAIL:-0}" = 0 ] || exit 1
    ;;
  '-f -')
    /bin/cat >> "$CTLVPS_NAT_TEST_INPUTS"
    ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &Manager{Bin: bin}, calls, inputs
}

func TestEnsureNATActivationPreflightAndRemoval(t *testing.T) {
	cfg := natFixtureConfig(natconfig.Mapping{PublicPort: 33001, LocalPort: 13001})
	nodes := []agentproto.NodeSpec{{Protocol: "ss", ListenPort: 33001}}
	t.Run("activation", func(t *testing.T) {
		m, calls, inputs := natManagerFixture(t, `{"nftables":[]}`)
		if err := m.EnsureNAT(context.Background(), cfg, nodes, nil); err != nil {
			t.Fatal(err)
		}
		gotCalls, _ := os.ReadFile(calls)
		if string(gotCalls) != "--check -f -\n-f -\n" {
			t.Fatal(string(gotCalls))
		}
		rules, _ := NATRules(cfg, nodes, nil)
		gotInputs, _ := os.ReadFile(inputs)
		if string(gotInputs) != rules+rules {
			t.Fatal("preflight and activation used different transactions", string(gotInputs))
		}
	})
	t.Run("failed preflight", func(t *testing.T) {
		m, calls, _ := natManagerFixture(t, `{"nftables":[]}`)
		t.Setenv("CTLVPS_NAT_TEST_CHECK_FAIL", "1")
		if err := m.EnsureNAT(context.Background(), cfg, nodes, nil); err == nil {
			t.Fatal("failed preflight accepted")
		}
		got, _ := os.ReadFile(calls)
		if string(got) != "--check -f -\n" {
			t.Fatal("applied after a failed preflight", string(got))
		}
	})
	t.Run("disabled removes owned table", func(t *testing.T) {
		m, calls, inputs := natManagerFixture(t, `{"nftables":[{"table":{"family":"inet","name":"ctlvps_nat"}},{"table":{"family":"inet","name":"administrator"}}]}`)
		if err := m.EnsureNAT(context.Background(), natFixtureConfig(), nil, nil); err != nil {
			t.Fatal(err)
		}
		gotCalls, _ := os.ReadFile(calls)
		gotInputs, _ := os.ReadFile(inputs)
		if string(gotCalls) != "-j list tables\n--check -f -\n-f -\n" || string(gotInputs) != "delete table inet ctlvps_nat\ndelete table inet ctlvps_nat\n" {
			t.Fatal(string(gotCalls), string(gotInputs))
		}
	})
}

func TestEnsureNATDisabledRequiresSuccessfulInventory(t *testing.T) {
	for _, tables := range []string{`{"nftables":[]}`, `{"nftables":[{"table":{"family":"ip","name":"ctlvps_nat"}},{"table":{"family":"inet","name":"administrator"}}]}`} {
		t.Run(tables, func(t *testing.T) {
			m, calls, inputs := natManagerFixture(t, tables)
			if err := m.EnsureNAT(context.Background(), natFixtureConfig(), nil, nil); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(calls)
			if string(got) != "-j list tables\n" {
				t.Fatal("disabled NAT created or modified a table", string(got))
			}
			if _, err := os.Stat(inputs); !os.IsNotExist(err) {
				t.Fatal("disabled NAT submitted a transaction", err)
			}
		})
	}
	for _, tables := range []string{"invalid", `{}`, `{"nftables":null}`, `{"nftables":[{"table":{}}]}`} {
		t.Run(tables, func(t *testing.T) {
			m, _, _ := natManagerFixture(t, tables)
			if err := m.EnsureNAT(context.Background(), natFixtureConfig(), nil, nil); err == nil {
				t.Fatal("unknown table state treated as absent")
			}
		})
	}
	t.Run("permission failure", func(t *testing.T) {
		m, _, _ := natManagerFixture(t, `{"nftables":[]}`)
		t.Setenv("CTLVPS_NAT_TEST_LIST_FAIL", "1")
		if err := m.EnsureNAT(context.Background(), natFixtureConfig(), nil, nil); err == nil {
			t.Fatal("nft permission failure treated as a missing table")
		}
	})
}
