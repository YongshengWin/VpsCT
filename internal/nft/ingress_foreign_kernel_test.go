package nft

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

func TestKernelIPv6NodeIngress(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_IPV6_INGRESS_TEST") != "1" {
		t.Skip("isolated Linux nftables test")
	}
	ifaces, err := net.Interfaces()
	if _, err := os.Stat("/.dockerenv"); err != nil || os.Geteuid() != 0 || len(ifaces) != 1 || ifaces[0].Name != "lo" {
		t.Fatal("requires root in a disposable container with --network none")
	}
	if err != nil {
		t.Fatal(err)
	}
	const host = "2606:4700:100::12"
	const port = "24443"
	for _, args := range [][]string{{"link", "set", "lo", "up"}, {"-6", "addr", "add", host + "/128", "dev", "lo", "nodad"}} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture address: %v %s", err, out)
		}
	}
	t.Cleanup(func() { _ = exec.Command("ip", "-6", "addr", "del", host+"/128", "dev", "lo").Run() })
	serveIngressProbe(t, "tcp", ":"+port)
	serveIngressProbe(t, "udp", ":"+port)
	for _, network := range []string{"tcp4", "udp4"} {
		if err := probeIngress(network, "127.0.0.1:"+port); err != nil {
			t.Fatal("fixture listener must accept IPv4 before the guard", err)
		}
	}
	ifaceID := strings.Repeat("a", 32)
	plan, err := networkguard.New([]networkguard.Binding{{
		NodeID: 1, ListenPort: 24443, Core: "singbox", IPv4Only: true,
		Wanted: networkconfig.Node{IngressFamily: "ipv6", ListenMode: "public_ipv6", ListenInterfaceID: ifaceID, AdvertiseMode: "inherit_ipv6", OnUnavailable: "block"},
		Applied: networkconfig.Resolved{ListenAddress: host, ListenInterface: &networkconfig.Interface{ID: ifaceID, Name: "lo", Index: ifaces[0].Index},
			CollectorID: strings.Repeat("b", 32), BootID: "fixture", Sequence: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := New()
	rules, err := plan.Rules()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.run(ctx, rules, "-f", "-"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.run(ctx, "", "delete", "table", "inet", networkguard.Table) })
	published := net.JoinHostPort(host, port)
	if err := probeIngress("tcp6", published); err == nil {
		t.Fatal("unconfirmed IPv6 listener accepted traffic")
	}
	rules, err = plan.RenewResources(map[agentproto.ResourceIdentity]bool{plan.Bindings[0].Resource(): true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.run(ctx, rules, "-f", "-"); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"tcp", "udp"} {
		if err := probeIngress(protocol+"6", published); err != nil {
			t.Fatal("verified IPv6 address was blocked", protocol, err)
		}
		for _, denied := range []struct{ network, host string }{{protocol + "4", "127.0.0.1"}, {protocol, "::ffff:127.0.0.1"}, {protocol + "6", "::1"}} {
			if err := probeIngress(denied.network, net.JoinHostPort(denied.host, port)); err == nil {
				t.Fatal("IPv6-only guard accepted a different endpoint", denied)
			}
		}
	}
}

// Run only in a disposable network namespace. Exercise real nft JSON and
// packet filtering for both transports and address families.
func TestKernelForeignIngress(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_INGRESS_TEST") != "1" {
		t.Skip("isolated Linux nftables test")
	}
	ctx := context.Background()
	m := New()
	for _, tc := range []struct{ name, table, network, address, protectedNetwork, protocol, port, seed string }{
		{"fail2ban", "inet issue_f2b", "tcp4", "127.0.0.1", "tcp4", "vless", "23456", `add table inet issue_f2b
add set inet issue_f2b blocked { type ipv4_addr; elements = { 127.0.0.1 }; }
add chain inet issue_f2b input { type filter hook input priority -1; policy accept; }
add rule inet issue_f2b input tcp dport 23459 ip saddr @blocked reject with icmpx type port-unreachable
`},
		{"iptables-shaped", "ip issue_filter", "tcp4", "127.0.0.1", "tcp4", "vless", "23456", `add table ip issue_filter
add chain ip issue_filter INPUT { type filter hook input priority 0; policy accept; }
add rule ip issue_filter INPUT ip saddr 192.0.2.0/24 tcp dport 23459 counter accept
add rule ip issue_filter INPUT tcp dport 23459 counter drop
`},
		{"ipv6-fail2ban", "inet issue_f2b6", "tcp6", "::1", "tcp6", "vless", "23456", `add table inet issue_f2b6
add set inet issue_f2b6 blocked { type ipv6_addr; elements = { ::1 }; }
add chain inet issue_f2b6 input { type filter hook input priority -1; policy accept; }
add rule inet issue_f2b6 input tcp dport 23459 ip6 saddr @blocked reject with icmpx type port-unreachable
`},
		{"udp-ipv4-range", "ip issue_udp", "udp4", "127.0.0.1", "udp4", "hysteria2", "23456", `add table ip issue_udp
add chain ip issue_udp INPUT { type filter hook input priority 0; policy accept; }
add rule ip issue_udp INPUT udp dport 23459-23462 counter drop
`},
		{"udp-ipv6-set", "ip6 issue_udp6", "udp6", "::1", "udp6", "hysteria2", "23456", `add table ip6 issue_udp6
add chain ip6 issue_udp6 INPUT { type filter hook input priority 0; policy accept; }
add rule ip6 issue_udp6 INPUT udp dport { 23459, 23460-23462 } counter drop
`},
		{"same-number-different-transport", "inet issue_transport", "udp4", "127.0.0.1", "tcp4", "hysteria2", "23459", `add table inet issue_transport
add chain inet issue_transport input { type filter hook input priority 0; policy accept; }
add rule inet issue_transport input tcp dport 23459 counter drop
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.run(ctx, "add table inet filter\nadd chain inet filter input { type filter hook input priority 0; policy accept; }\n"+tc.seed, "-f", "-"); err != nil {
				t.Fatal(err)
			}
			defer m.run(ctx, "", "delete", "table", "inet", "filter")
			defer func() { _, _ = m.run(ctx, "delete table "+tc.table+"\n", "-f", "-") }()
			nodeAddress := net.JoinHostPort(tc.address, tc.port)
			protectedAddress := net.JoinHostPort(tc.address, "23459")
			serveIngressProbe(t, tc.network, nodeAddress)
			serveIngressProbe(t, tc.protectedNetwork, protectedAddress)
			port, err := strconv.Atoi(tc.port)
			if err != nil {
				t.Fatal(err)
			}
			nodes := []agentproto.NodeSpec{{Protocol: tc.protocol, ListenPort: port}}
			for i := 0; i < 2; i++ {
				if err := m.EnsureIngress(ctx, nodes); err != nil {
					t.Fatal(err)
				}
			}
			if err := probeIngress(tc.network, nodeAddress); err != nil {
				t.Fatal("node blocked", err)
			}
			if err := probeIngress(tc.protectedNetwork, protectedAddress); err == nil {
				t.Fatal("foreign protection bypassed")
			}
			protectedProtocol := "vless"
			if strings.HasPrefix(tc.protectedNetwork, "udp") {
				protectedProtocol = "hysteria2"
			}
			if err := m.EnsureIngress(ctx, []agentproto.NodeSpec{{Protocol: protectedProtocol, ListenPort: 23459}}); err == nil {
				t.Fatal("overlap accepted")
			}
			// A ruleset reload must not turn a previously safe proof into a cached
			// permanent exemption: add a rule covering the node's actual transport.
			transport := "tcp"
			if strings.HasPrefix(tc.network, "udp") {
				transport = "udp"
			}
			table := strings.Fields(tc.table)
			chain := "INPUT"
			if table[1] == "issue_f2b" || table[1] == "issue_f2b6" || table[1] == "issue_transport" {
				chain = "input"
			}
			if _, err := m.run(ctx, "add rule "+tc.table+" "+chain+" "+transport+" dport "+tc.port+" drop\n", "-f", "-"); err != nil {
				t.Fatal(err)
			}
			if err := m.EnsureIngress(ctx, nodes); err == nil {
				t.Fatal("changed foreign ruleset accepted from stale proof")
			}
			if err := m.EnsureIngress(ctx, nil); err != nil {
				t.Fatal(err)
			}
			listing, err := m.run(ctx, "", "list", "ruleset")
			if err != nil || strings.Contains(string(listing), ingressComment) || !strings.Contains(string(listing), "23459") {
				t.Fatalf("foreign rules lost: %s %v", listing, err)
			}
		})
	}
}

func serveIngressProbe(t *testing.T, network, address string) {
	t.Helper()
	if strings.HasPrefix(network, "tcp") {
		listener, err := net.Listen(network, address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		return
	}
	listener, err := net.ListenPacket(network, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		buf := make([]byte, 32)
		for {
			n, peer, err := listener.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = listener.WriteTo(buf[:n], peer)
		}
	}()
}

func probeIngress(network, address string) error {
	conn, err := net.DialTimeout(network, address, 200*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	if strings.HasPrefix(network, "tcp") {
		return nil
	}
	if err = conn.SetDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		return err
	}
	if _, err = conn.Write([]byte("probe")); err != nil {
		return err
	}
	buf := make([]byte, 32)
	_, err = conn.Read(buf)
	return err
}
