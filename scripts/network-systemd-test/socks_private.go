//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/proxyguard"
)

func testPrivateSOCKSRuntime(ctx context.Context, nodes []domain.Node, grants []networkconfig.TransportGrant, setGrants func([]networkconfig.TransportGrant), load func() *networkguard.Plan, find func(*networkguard.Plan, int64) networkguard.Binding, verify func(int, string), primary string) {
	// The fixture origin really accepts this private destination. Rejection
	// through SOCKS therefore proves business policy, not missing reachability.
	run("ip", "netns", "exec", "landing", "/fixtures/probe", "listener-probe", "10.44.0.10:18080")
	for _, target := range []string{"10.44.0.10", "private-business.fixture.test"} {
		for _, transport := range []string{"tcp", "udp"} {
			if _, err := probe(0, transport, target); err == nil {
				panic("transport authorization widened private business access")
			}
		}
	}
	pin := find(load(), nodes[0].ID).Applied.SOCKS5
	if pin == nil || pin.Address != primary || len(pin.TransportGrants) != 2 || pin.UDPPorts()[0] != (networkconfig.TransportPortRange{First: 40000, Last: 60000}) {
		panic("applied transport did not intersect private root authorization")
	}
	fmt.Println("PASS private SOCKS and bootstrap DNS use separate root grants; private literal/domain business remains denied")

	stopFlows := process(ctx, "ip", "netns", "exec", "landing", "/fixtures/probe", "held-flows")
	defer stopFlows()
	eventually("established private TCP/UDP fixture", func() bool { _, err := os.Stat("/tmp/fixture-flows-ready"); return err == nil })
	setGrants(grants[2:]) // retain DNS authority, revoke only the SOCKS transport.
	eventually("private transport revoked offline", func() bool { return find(load(), nodes[0].ID).Pending })
	must(os.WriteFile("/tmp/fixture-flows-check", []byte("check"), 0600))
	eventually("existing private TCP/UDP blocked", func() bool { _, err := os.Stat("/tmp/fixture-flows-blocked"); return err == nil })
	for _, transport := range []string{"tcp", "udp"} {
		if _, err := probe(0, transport, "203.0.113.10"); err == nil {
			panic("revoked private transport still forwards")
		}
	}
	verify(1, "198.51.100.2")
	setGrants(grants)
	eventually("private root grant restored without controller", func() bool {
		b := find(load(), nodes[0].ID)
		return !b.Pending && b.Applied.SOCKS5 != nil
	})
	verify(0, primary)
	fmt.Println("PASS offline root grant revocation stops established and new TCP/UDP; peer survives; restored grant reapplies")
	narrow := append([]networkconfig.TransportGrant(nil), grants...)
	narrow[1].Port, narrow[1].PortEnd = 1, 0 // the real relay allocates only 40000–60000.
	setGrants(narrow)
	eventually("changed private UDP range reapplied", func() bool {
		b := find(load(), nodes[0].ID)
		return !b.Pending && b.Applied.SOCKS5 != nil && len(b.Applied.SOCKS5.UDPPorts()) == 1 && b.Applied.SOCKS5.UDPPorts()[0].First == 1
	})
	if got, err := probe(0, "tcp", "203.0.113.10"); err != nil || got != primary {
		panic("UDP grant narrowing broke independently authorized TCP")
	}
	if _, err := probe(0, "udp", "203.0.113.10"); err == nil {
		panic("private UDP relay escaped its authorized port range")
	}
	setGrants(grants)
	eventually("private UDP grant restored offline", func() bool {
		b := find(load(), nodes[0].ID)
		return !b.Pending && b.Applied.SOCKS5 != nil && len(b.Applied.SOCKS5.UDPPorts()) == 1 && b.Applied.SOCKS5.UDPPorts()[0].First == 40000
	})
	verify(0, primary)
	verify(1, "198.51.100.2")
	fmt.Println("PASS private UDP port narrowing blocks only out-of-range relays while authorized TCP works")

	// Pausing only the agent prevents its monitor from supplying new leases
	// during inspection. The independently invoked root watchdog must repair
	// both tables, and must not preserve old health across that repair.
	run("systemctl", "kill", "--kill-who=main", "--signal=SIGSTOP", "ctlvps-agent.service")
	defer run("systemctl", "kill", "--kill-who=main", "--signal=SIGCONT", "ctlvps-agent.service")
	run("nft", "flush", "chain", "inet", "ctlvps_egress", "ctlvps_transports")
	run("nft", "flush", "chain", "inet", networkguard.Table, "output")
	must(proxyguard.Check(ctx))
	general := run("nft", "list", "chain", "inet", "ctlvps_egress", "ctlvps_transports")
	if !strings.Contains(general, primary) || strings.Contains(general, "10.23.0.0/24") || !strings.Contains(general, "40000-60000") {
		panic("watchdog restored wrong transport exception")
	}
	if _, err := probe(0, "tcp", "203.0.113.10"); err == nil {
		panic("watchdog restored stale transport health")
	}
	run("systemctl", "kill", "--kill-who=main", "--signal=SIGCONT", "ctlvps-agent.service")
	eventually("fresh monitor renews repaired private guard", func() bool { got, err := probe(0, "tcp", "203.0.113.10"); return err == nil && got == primary })
	verify(0, primary)
	verify(1, "198.51.100.2")
	fmt.Println("PASS watchdog repairs both firewall tables atomically with empty leases; fresh monitor restores traffic")
}
