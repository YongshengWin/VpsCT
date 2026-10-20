package nft

import (
	"context"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
)

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
