//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

// Minimal authoritative fixture: one question, A/AAAA records, no recursion or
// network dependencies. A special name exercises UDP truncation and TCP retry.
func dnsAnswer(q []byte, tcp bool) []byte {
	if len(q) < 17 || binary.BigEndian.Uint16(q[4:6]) != 1 {
		return nil
	}
	end := 12
	for end < len(q) && q[end] != 0 {
		n := int(q[end])
		if n > 63 || end+1+n >= len(q) {
			return nil
		}
		end += n + 1
	}
	end++
	if end+4 > len(q) {
		return nil
	}
	typ := binary.BigEndian.Uint16(q[end : end+2])
	b := append([]byte(nil), q[:end+4]...)
	binary.BigEndian.PutUint16(b[2:4], 0x8180)
	for i := 6; i < 12; i++ {
		b[i] = 0
	}
	if !tcp && strings.Contains(string(q[12:end]), "truncate") {
		binary.BigEndian.PutUint16(b[2:4], 0x8380)
		return b
	}
	var ip []byte
	if typ == 1 {
		address := "203.0.113.10"
		if strings.Contains(string(q[12:end]), "forward-refresh") {
			mode, _ := os.ReadFile("/tmp/ctlvps-fixture-forward-dns")
			if string(mode) == "private" {
				address = "10.0.0.1"
			}
		}
		if strings.Contains(string(q[12:end]), "refresh0") {
			address = "192.0.2.2"
			if os.Getenv("NETWORK_SYSTEMD_CASE") == "socks-private" {
				address = "10.23.0.2"
			}
			mode, _ := os.ReadFile("/tmp/ctlvps-fixture-dns-rebind")
			if string(mode) == "alternate" {
				address = "192.0.2.3"
				if os.Getenv("NETWORK_SYSTEMD_CASE") == "socks-private" {
					address = "10.23.0.3"
				}
			}
			if string(mode) == "private" {
				address = "10.0.0.1"
			}
		}
		if strings.Contains(string(q[12:end]), "refresh1") {
			address = "198.51.100.2"
		}
		if strings.Contains(string(q[12:end]), "upstream0") {
			address = "198.51.100.2"
		}
		if strings.Contains(string(q[12:end]), "upstream1") {
			address = "192.0.2.2"
		}
		if strings.Contains(string(q[12:end]), "private-business") {
			address = "10.44.0.10"
		}
		ip = net.ParseIP(address).To4()
	} else if typ == 28 {
		address := "2001:db8:ffff::10"
		if strings.Contains(string(q[12:end]), "upstream0") {
			address = "2001:db8:2::2"
		}
		if strings.Contains(string(q[12:end]), "upstream1") {
			address = "2001:db8:1::2"
		}
		ip = net.ParseIP(address).To16()
	} else {
		return b
	}
	binary.BigEndian.PutUint16(b[6:8], 1)
	b = append(b, 0xc0, 0x0c, byte(typ>>8), byte(typ), 0, 1, 0, 0, 0, 60, 0, byte(len(ip)))
	if strings.Contains(string(q[12:end]), "refresh") {
		b[len(b)-3] = 30
	}
	return append(b, ip...)
}

func serveDNS() {
	addresses := []string{"203.0.113.10:15353", "[2001:db8:ffff::10]:15353", "192.0.2.2:15353", "198.51.100.2:15353"}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "socks-private" {
		addresses = append(addresses, "10.23.0.2:15353")
	}
	for _, addr := range addresses {
		u, err := net.ListenPacket("udp", addr)
		must(err)
		go func() {
			b := make([]byte, 4096)
			for {
				n, peer, err := u.ReadFrom(b)
				if err != nil {
					return
				}
				if answer := dnsAnswer(b[:n], false); answer != nil {
					_, _ = u.WriteTo(answer, peer)
				}
			}
		}()
		l, err := net.Listen("tcp", addr)
		must(err)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					var size [2]byte
					for {
						if _, err := io.ReadFull(c, size[:]); err != nil {
							return
						}
						length := binary.BigEndian.Uint16(size[:])
						if length > 4096 {
							return
						}
						q := make([]byte, length)
						if _, err := io.ReadFull(c, q); err != nil {
							return
						}
						answer := dnsAnswer(q, true)
						if answer == nil {
							return
						}
						binary.BigEndian.PutUint16(size[:], uint16(len(answer)))
						if _, err := c.Write(append(size[:], answer...)); err != nil {
							return
						}
					}
				}()
			}
		}()
	}
}

func launch(ctx context.Context, exe, name string, cfg map[string]any) func() {
	data, err := json.Marshal(cfg)
	must(err)
	file := filepath.Join("/tmp", name+".json")
	must(os.WriteFile(file, data, 0644))
	log, err := os.Create(filepath.Join("/tmp", name+".log"))
	must(err)
	cmd := exec.CommandContext(ctx, "setpriv", "--reuid=65534", "--regid=65534", "--clear-groups", "--bounding-set=-all,+net_bind_service,+net_raw", "--inh-caps=+net_bind_service,+net_raw", "--ambient-caps=+net_bind_service,+net_raw", exe, "launch", file)
	cmd.Stdout, cmd.Stderr = log, log
	must(cmd.Start())
	stop := func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() }
	for n := 0; n < 50; n++ {
		c, err := net.DialTimeout("tcp", "127.0.0.1:1080", 50*time.Millisecond)
		if err == nil {
			c.Close()
			return stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	b, _ := os.ReadFile(log.Name())
	panic(fmt.Sprintf("%s failed to start: %s", name, b))
}

func twoNodes() map[string]any {
	return twoNodesWithDNS("udp", "dual")
}

func twoNodesWithDNS(transport, family string) map[string]any {
	dir, err := os.MkdirTemp("/tmp", "compile-")
	must(err)
	defer os.RemoveAll(dir)
	collector := netinventory.New(dir, "fixture-boot")
	defer collector.Close()
	snapshot := collector.Collect()
	cfg, _ := compileNodes(snapshot, transport, family)
	return cfg
}

func compileNodes(snapshot *agentproto.NetworkSnapshot, transport, family string) (map[string]any, networkguard.Plan) {
	if snapshot.Status != "ok" {
		panic("compiler fixture inventory incomplete")
	}
	inbounds, outbounds, rules, servers := []any{}, []any{}, []any{}, []any{}
	bindings := []networkguard.Binding{}
	for i := 0; i < 2; i++ {
		nodeID := int64(257 + i)
		tag, name, id := core.InboundTag(nodeID), fmt.Sprintf("wan%d", 1-i), ""
		for _, iface := range snapshot.Interfaces {
			if iface.Name == name {
				id = iface.ID
			}
		}
		if id == "" {
			panic("compiler fixture interface missing")
		}
		direct := networkconfig.Direct{InterfaceID: id, Family: family, DNS: networkconfig.Resolver{Transport: transport, Address: "203.0.113.10", Port: 15353}}
		if family == "ipv6" {
			direct.DNS.Address = "2001:db8:ffff::10"
		}
		if family != "ipv6" {
			source := "198.51.100.1"
			if i == 1 {
				source = "192.0.2.1"
			}
			direct.SourceIPv4 = &networkconfig.Address{InterfaceID: id, Address: source}
		}
		if family != "ipv4" {
			direct.SourceIPv6 = &networkconfig.Address{InterfaceID: id, Address: fmt.Sprintf("2001:db8:%d::1", 2-i)}
		}
		node := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}
		resolved, err := netinventory.ResolveBinding(node, &direct, snapshot, false, time.Now())
		must(err)
		compiled, err := core.CompileDirect(nodeID, *resolved.Direct)
		must(err)
		bindings = append(bindings, networkguard.Binding{NodeID: nodeID, ListenPort: 1080 + i, Core: "singbox", Wanted: node, Applied: resolved})
		inbounds = append(inbounds, map[string]any{"type": "socks", "tag": tag, "listen": "127.0.0.1", "listen_port": 1080 + i})
		outbounds = append(outbounds, compiled.Outbound)
		servers = append(servers, compiled.DNS)
		if compiled.FamilyReject != nil {
			rules = append(rules, compiled.FamilyReject)
		}
		rules = append(rules, compiled.ResolveRule, map[string]any{"inbound": []string{tag}, "action": "route", "outbound": compiled.Outbound["tag"]})
	}
	plan, err := networkguard.New(bindings)
	must(err)
	return map[string]any{"log": map[string]any{"level": "error"}, "inbounds": inbounds, "outbounds": outbounds, "dns": map[string]any{"servers": servers, "independent_cache": true}, "route": map[string]any{"rules": rules}}, plan
}

func resetProbe() {
	run("nft", "flush", "chain", "inet", "bind_probe", "output")
	run("nft", "reset", "counters", "table", "inet", "bind_probe")
}

func testDNS(ctx context.Context, exe string) {
	for _, name := range []string{"dns0", "dns1", "dns_wrong", "dns_tcp"} {
		run("nft", "add", "counter", "inet", "bind_probe", name)
	}
	for _, transport := range []string{"udp", "tcp"} {
		for _, family := range []string{"4", "6"} {
			resetProbe()
			cfg := twoNodesWithDNS(transport, "ipv"+family)
			for i := 0; i < 2; i++ {
				dns := fmt.Sprintf("dns%d", i)
				iface, mark := fmt.Sprintf("wan%d", 1-i), fmt.Sprint(0x43000101+i)
				run("nft", "add", "rule", "inet", "bind_probe", "output", "meta", "mark", mark, "meta", "l4proto", "{ tcp, udp }", "th", "dport", "15353", "counter", "name", dns)
				run("nft", "add", "rule", "inet", "bind_probe", "output", "meta", "mark", mark, "oifname", "!=", fmt.Sprintf("%q", iface), "counter", "name", "dns_wrong")
			}
			run("nft", "add", "rule", "inet", "bind_probe", "output", "meta", "mark", "!=", "{ 0x43000101, 0x43000102 }", "meta", "l4proto", "{ tcp, udp }", "th", "dport", "15353", "counter", "name", "dns_wrong")
			run("nft", "add", "rule", "inet", "bind_probe", "output", "tcp", "dport", "15353", "counter", "name", "dns_tcp")
			name := "DNS-" + transport + "-IPv" + family
			stop := launch(ctx, exe, name, cfg)
			func() {
				defer stop()
				// A literal target bypasses DNS strategy. The compiled routing rule
				// must still reject the disabled address family for both transports.
				disabled := "2001:db8:ffff::10"
				if family == "6" {
					disabled = "203.0.113.10"
				}
				if _, err := tcp(disabled); err == nil {
					panic("literal TCP target bypassed the address-family policy")
				}
				if _, err := udp(disabled); err == nil {
					panic("literal UDP target bypassed the address-family policy")
				}
				for _, domain := range []string{"binding.test", "truncate.binding.test"} {
					for i := 0; i < 2; i++ {
						counter := fmt.Sprintf("dns%d", i)
						before := packets(counter)
						c, _, _, err := socksAt(1080+i, 1, domain)
						must(err)
						got, err := bufio.NewReader(c).ReadString('\n')
						c.Close()
						must(err)
						want := "198.51.100.1"
						if i == 1 {
							want = "192.0.2.1"
						}
						if family == "6" {
							want = fmt.Sprintf("2001:db8:%d::1", 2-i)
						}
						if strings.TrimSpace(got) != want || packets(counter) <= before || packets("dns_wrong") != 0 {
							panic(fmt.Sprintf("%s node%d lost DNS attribution/isolation or used the wrong interface: got=%q", name, i, got))
						}
						got, err = udpAt(1080+i, domain)
						if err != nil || got != want || packets("dns_wrong") != 0 {
							panic(fmt.Sprintf("%s node%d UDP domain routing failed: got=%q err=%v", name, i, got, err))
						}
					}
				}
				if packets("dns_tcp") == 0 {
					panic("truncated UDP DNS did not retry TCP")
				}
				// A fresh name prevents a cached answer from hiding DNS fallback.
				run("ip", "link", "set", "wan1", "down")
				c, _, _, err := socksAt(1080, 1, "fresh.binding.test")
				if err == nil {
					b := make([]byte, 128)
					_, err = c.Read(b)
					c.Close()
				}
				if err == nil || packets("dns_wrong") != 0 {
					panic("DNS used an alternate interface")
				}
				c, _, _, err = socksAt(1081, 1, "fresh.binding.test")
				must(err)
				_, err = bufio.NewReader(c).ReadString('\n')
				c.Close()
				must(err)
				restoreWan1()
			}()
			fmt.Println("PASS", name, "separate node DNS caches/marks, TCP retry, fail-closed without stopping peer")
		}
	}
}

type flow struct {
	control          net.Conn
	data             net.Conn
	reader           *bufio.Reader
	target, protocol string
}

func openFlow(target, protocol string) *flow {
	f := &flow{target: target, protocol: protocol}
	if protocol == "TCP" {
		c, _, _, err := socks(1, target)
		must(err)
		f.data, f.reader = c, bufio.NewReader(c)
		_, err = f.reader.ReadString('\n')
		must(err)
	} else {
		c, ip, port, err := socks(3, target)
		must(err)
		f.control = c
		if net.ParseIP(ip).IsUnspecified() {
			ip = "127.0.0.1"
		}
		f.data, err = net.Dial("udp", net.JoinHostPort(ip, fmt.Sprint(port)))
		must(err)
	}
	return f
}
func (f *flow) close() {
	f.data.Close()
	if f.control != nil {
		f.control.Close()
	}
}
func (f *flow) ping() error {
	f.data.SetDeadline(time.Now().Add(500 * time.Millisecond))
	b := []byte("ping\n")
	if f.protocol == "UDP" {
		b = append(append([]byte{0, 0, 0}, address(f.target, 18080)...), b...)
	}
	if _, err := f.data.Write(b); err != nil {
		return err
	}
	if f.reader != nil {
		_, err := f.reader.ReadString('\n')
		return err
	}
	n, err := f.data.Read(make([]byte, 2048))
	if err == nil && n < 10 {
		return fmt.Errorf("short flow reply")
	}
	return err
}

func testEstablished(ctx context.Context, exe string) {
	for _, fault := range []string{"link-down", "guard-block", "replace-same-ifindex"} {
		resetProbe()
		for _, iface := range []string{"wan0", "wan1"} {
			run("nft", "add", "rule", "inet", "bind_probe", "output", "meta", "mark", "0x43000101", "oifname", fmt.Sprintf("%q", iface), "counter", "name", iface)
		}
		stop := launch(ctx, exe, fault, twoNodes())
		func() {
			defer stop()
			flows := []*flow{}
			for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10"} {
				for _, proto := range []string{"TCP", "UDP"} {
					f := openFlow(target, proto)
					defer f.close()
					must(f.ping())
					flows = append(flows, f)
				}
			}
			if fault == "link-down" {
				run("ip", "link", "set", "wan1", "down")
			} else {
				// The production guard must apply this fence before acknowledging
				// an unavailable identity; the fixture proves it covers old flows.
				run("nft", "insert", "rule", "inet", "bind_probe", "output", "meta", "mark", "0x43000101", "drop")
				if fault == "replace-same-ifindex" {
					replaceInterface()
				}
			}
			for _, f := range flows {
				if f.ping() == nil {
					panic(fmt.Sprintf("%s did not stop existing %s %s", fault, f.protocol, f.target))
				}
			}
			if packets("wan0") != 0 {
				panic(fault + " fell back to wan0")
			}
			c, _, _, err := socksAt(1081, 1, "203.0.113.10")
			must(err)
			_, err = bufio.NewReader(c).ReadString('\n')
			c.Close()
			must(err)
		}()
		restoreWan1()
		fmt.Println("PASS", fault, "blocks existing TCP/UDP on IPv4/IPv6 and preserves the other node")
	}
}

func replaceInterface() {
	dir, err := os.MkdirTemp("/tmp", "identity-")
	must(err)
	defer os.RemoveAll(dir)
	c := netinventory.New(dir, "fixture-boot")
	defer c.Close()
	before := c.Collect()
	if before.Status != "ok" {
		panic("identity inventory failed")
	}
	index, oldID := 0, ""
	for _, iface := range before.Interfaces {
		if iface.Name == "wan1" {
			index, oldID = iface.Index, iface.ID
		}
	}
	if index == 0 {
		panic("missing wan1")
	}
	run("ip", "link", "del", "wan1")
	run("ip", "link", "add", "wan1", "index", fmt.Sprint(index), "type", "veth", "peer", "name", "peer1")
	run("ip", "link", "set", "peer1", "netns", "landing")
	run("ip", "addr", "add", "198.51.100.1/24", "dev", "wan1")
	run("ip", "-6", "addr", "add", "2001:db8:2::1/64", "dev", "wan1", "nodad")
	run("ip", "link", "set", "wan1", "up")
	run("ip", "netns", "exec", "landing", "ip", "addr", "add", "198.51.100.2/24", "dev", "peer1")
	run("ip", "netns", "exec", "landing", "ip", "-6", "addr", "add", "2001:db8:2::2/64", "dev", "peer1", "nodad")
	run("ip", "netns", "exec", "landing", "ip", "link", "set", "peer1", "up")
	run("ip", "route", "add", "203.0.113.10/32", "via", "198.51.100.2", "dev", "wan1", "metric", "200")
	run("ip", "-6", "route", "add", "2001:db8:ffff::10/128", "via", "2001:db8:2::2", "dev", "wan1", "metric", "200")
	after := c.Collect()
	if after.Status != "ok" {
		panic("replacement inventory failed")
	}
	for _, iface := range after.Interfaces {
		if iface.Name == "wan1" && iface.ID != oldID && iface.Index == index {
			return
		}
	}
	panic("same-ifindex replacement reused interface identity")
}

func restoreWan1() {
	run("ip", "link", "set", "wan1", "up")
	// The fixture kernel withdraws IPv6 addresses/routes when the link is
	// lowered. Restore them explicitly; production must never repair these.
	run("ip", "-6", "addr", "replace", "2001:db8:2::1/64", "dev", "wan1", "nodad")
	run("ip", "-6", "route", "replace", "2001:db8:ffff::10/128", "via", "2001:db8:2::2", "dev", "wan1", "metric", "200")
}
