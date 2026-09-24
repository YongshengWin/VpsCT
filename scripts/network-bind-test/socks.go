//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/nft"
)

// SOCKS only exists inside this disconnected fixture. Production must never
// provision an unauthenticated public SOCKS listener as a side effect.
func socksUpstreamConfig() map[string]any {
	ins, outs, rules := []any{}, []any{}, []any{}
	for i := 0; i < 2; i++ {
		ip := []string{"198.51.100.2", "192.0.2.2"}[i]
		tag := fmt.Sprintf("upstream-%d", i)
		ins = append(ins, map[string]any{"type": "socks", "tag": tag, "listen": ip, "listen_port": 11080 + i,
			"users": []any{map[string]any{"username": "fixture", "password": "fixture-only-password"}}})
		outs = append(outs, map[string]any{"type": "direct", "tag": tag, "inet4_bind_address": ip, "inet6_bind_address": fmt.Sprintf("2001:db8:%d::2", 2-i)})
		rules = append(rules, map[string]any{"inbound": []string{tag}, "action": "route", "outbound": tag})
	}
	return map[string]any{"log": map[string]any{"level": "error"}, "inbounds": ins, "outbounds": outs, "route": map[string]any{"rules": rules}}
}

func socksConsumerConfig(dnsTransport, family string, udpEnabled, badPassword bool) map[string]any {
	dir, err := os.MkdirTemp("/tmp", "socks-compiler-")
	must(err)
	defer os.RemoveAll(dir)
	collector := netinventory.New(dir, "fixture-boot")
	defer collector.Close()
	snapshot := collector.Collect()
	ins, outs, rules, dns := []any{}, []any{}, []any{}, []any{}
	for i := 0; i < 2; i++ {
		id := int64(257 + i)
		tag := core.InboundTag(id)
		ip := []string{"198.51.100.2", "192.0.2.2"}[i]
		ifaceID := ""
		for _, iface := range snapshot.Interfaces {
			if iface.Name == fmt.Sprintf("wan%d", 1-i) {
				ifaceID = iface.ID
			}
		}
		if ifaceID == "" {
			panic("SOCKS compiler interface missing")
		}
		cfg := networkconfig.SOCKS5{
			Server: fmt.Sprintf("upstream%d.fixture.test", i), ServerPort: 11080 + i,
			Authentication: "password", UDP: udpEnabled, Family: family, ConnectTimeoutSeconds: 1,
			DNS:   networkconfig.Resolver{Transport: dnsTransport, Address: "203.0.113.10", Port: 15353},
			Outer: networkconfig.Direct{InterfaceID: ifaceID, Family: "ipv4", SourceIPv4: &networkconfig.Address{InterfaceID: ifaceID, Address: []string{"198.51.100.1", "192.0.2.1"}[i]}, DNS: networkconfig.Resolver{Transport: "tcp", Address: ip, Port: 15353}},
		}
		if family == "ipv6" {
			cfg.DNS.Address = "2001:db8:ffff::10"
		}
		credentials := networkconfig.SOCKS5Credentials{Username: "fixture", Password: "fixture-only-password"}
		if badPassword {
			credentials.Password = "incorrect-fixture-password"
		}
		policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}
		resolved, err := netinventory.ResolveBinding(policy, &cfg.Outer, snapshot, false, time.Now())
		must(err)
		compiled, err := core.CompileSOCKS5(id, cfg, credentials, *resolved.Direct)
		must(err)
		if compiled.UDPReject != nil {
			rules = append(rules, compiled.UDPReject)
		}
		if compiled.FamilyReject != nil {
			rules = append(rules, compiled.FamilyReject)
		}
		ins = append(ins, map[string]any{"type": "socks", "tag": tag, "listen": "127.0.0.1", "listen_port": 1080 + i})
		outs = append(outs, compiled.Outbound)
		dns = append(dns, compiled.BootstrapDNS, compiled.DNS)
		rules = append(rules, compiled.ResolveRule, map[string]any{"inbound": []string{tag}, "action": "route", "outbound": compiled.Outbound["tag"]})
	}
	return map[string]any{"log": map[string]any{"level": "error"}, "inbounds": ins, "outbounds": outs, "dns": map[string]any{"servers": dns, "independent_cache": true}, "route": map[string]any{"rules": rules}}
}

func socksTCPAt(port int, target string) (string, error) {
	c, _, _, err := socksAt(port, 1, target)
	if err != nil {
		return "", err
	}
	defer c.Close()
	line, err := bufio.NewReader(c).ReadString('\n')
	return line, err
}

func testSOCKSEgress(ctx context.Context, exe string) {
	stopUpstream := launchProduction(ctx, exe, "socks-upstream", "landing", socksUpstreamConfig())
	defer stopUpstream()
	for i := 0; i < 50; i++ {
		c, err := net.DialTimeout("tcp", "198.51.100.2:11080", 50*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	nodes := []agentproto.NodeSpec{{NodeID: 257, Core: "singbox", ListenPort: 1080}, {NodeID: 258, Core: "singbox", ListenPort: 1081}}
	rules, err := nft.NodeRules(nodes)
	must(err)
	nftInput(rules)
	for _, name := range []string{"leak", "wrong_mark", "node0_tcp", "node0_udp", "node1_tcp", "node1_udp"} {
		run("nft", "add", "counter", "inet", "bind_probe", name)
	}
	run("nft", "add", "rule", "inet", "bind_probe", "output", "oifname", "!=", "\"lo\"", "meta", "l4proto", "{ tcp, udp }", "ip", "daddr", "203.0.113.10", "th", "dport", "{ 15353, 18080 }", "counter", "name", "leak")
	run("nft", "add", "rule", "inet", "bind_probe", "output", "oifname", "!=", "\"lo\"", "meta", "l4proto", "{ tcp, udp }", "meta", "mark", "!=", "{ 0x43000101, 0x43000102 }", "counter", "name", "wrong_mark")
	for i := 0; i < 2; i++ {
		for _, transport := range []string{"tcp", "udp"} {
			run("nft", "add", "rule", "inet", "bind_probe", "output", "meta", "mark", fmt.Sprint(0x43000101+i), "oifname", fmt.Sprintf("\"wan%d\"", 1-i), "meta", "l4proto", transport, "counter", "name", fmt.Sprintf("node%d_%s", i, transport))
		}
	}
	for _, dnsTransport := range []string{"tcp", "udp"} {
		stop := launch(ctx, exe, "socks-"+dnsTransport, socksConsumerConfig(dnsTransport, "dual", true, false))
		for i := 0; i < 2; i++ {
			for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10", "echo.fixture.test", "truncate.fixture.test"} {
				want := []string{"198.51.100.2", "192.0.2.2"}[i]
				if target == "2001:db8:ffff::10" {
					want = fmt.Sprintf("2001:db8:%d::2", 2-i)
				}
				got, err := socksTCPAt(1080+i, target)
				if err != nil || got != want+"\n" {
					panic(fmt.Sprintf("SOCKS TCP DNS=%s node=%d target=%s got=%q want=%q err=%v", dnsTransport, i, target, got, want, err))
				}
				got, err = udpAt(1080+i, target)
				if err != nil || got != want {
					panic(fmt.Sprintf("SOCKS UDP DNS=%s node=%d target=%s got=%q want=%q err=%v", dnsTransport, i, target, got, want, err))
				}
			}
		}
		stop()
		fmt.Println("PASS SOCKS5 auth TCP/UDP IPv4/IPv6 and", dnsTransport, "DNS detour including truncated DNS fallback")
	}
	for _, name := range []string{"leak", "wrong_mark"} {
		if packets(name) != 0 {
			panic(fmt.Sprintf("SOCKS %s=%d", name, packets(name)))
		}
	}
	for _, name := range []string{"node0_tcp", "node0_udp", "node1_tcp", "node1_udp"} {
		if packets(name) == 0 {
			panic("missing per-node socket mark: " + name)
		}
	}
	fmt.Println("PASS independent marked TCP control and UDP data sockets, interface binding, no direct DNS/business leak")
	stop := launch(ctx, exe, "socks-wrong-auth", socksConsumerConfig("tcp", "dual", true, true))
	if _, err := socksTCPAt(1080, "203.0.113.10"); err == nil {
		panic("incorrect SOCKS credentials accepted TCP")
	}
	if _, err := udpAt(1080, "203.0.113.10"); err == nil {
		panic("incorrect SOCKS credentials accepted UDP")
	}
	stop()
	stop = launch(ctx, exe, "socks-tcp-only", socksConsumerConfig("tcp", "dual", false, false))
	_, err = socksTCPAt(1080, "echo.fixture.test")
	must(err)
	if _, err := udpAt(1080, "203.0.113.10"); err == nil {
		panic("TCP-only SOCKS allowed UDP")
	}
	stop()
	fmt.Println("PASS incorrect authentication rejected; TCP-only explicitly rejects UDP")
	stop = launch(ctx, exe, "socks-established", socksConsumerConfig("tcp", "dual", true, false))
	defer stop()
	flows := []*flow{}
	for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10"} {
		for _, transport := range []string{"TCP", "UDP"} {
			f := openFlow(target, transport)
			defer f.close()
			must(f.ping())
			flows = append(flows, f)
		}
	}
	nodes[0].Blocked = true
	rules, err = nft.NodeRules(nodes)
	must(err)
	nftInput(rules)
	for _, f := range flows {
		if f.ping() == nil {
			panic("quota fence failed to stop SOCKS " + f.protocol)
		}
	}
	_, err = socksTCPAt(1081, "203.0.113.10")
	must(err)
	_, err = udpAt(1081, "2001:db8:ffff::10")
	must(err)
	b, err := exec.Command("nft", "-j", "list", "counters", "table", "inet", nft.NodeTable).Output()
	must(err)
	var counters struct {
		Nftables []struct {
			Counter struct {
				Name  string
				Bytes int64
			}
		}
	}
	must(json.Unmarshal(b, &counters))
	seen := map[string]bool{}
	for _, record := range counters.Nftables {
		if record.Counter.Bytes > 0 {
			seen[record.Counter.Name] = true
		}
	}
	for _, name := range []string{"n257_rx", "n257_tx", "n258_rx", "n258_tx"} {
		if !seen[name] {
			panic("missing production accounting bytes: " + name)
		}
	}
	fmt.Println("PASS production nft quota fence closes existing SOCKS TCP/UDP IPv4/IPv6 while peer node stays available")
}
