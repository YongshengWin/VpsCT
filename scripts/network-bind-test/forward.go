//go:build linux

package main

// This pre-integration experiment proves capabilities of the pinned core and
// kernel. Its nft table is fixture-only: it does NOT replace the production
// root watchdog, durable apply transaction, accounting ledger or API checks.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/nft"
)

type forwardReply struct {
	Value string `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

func forwardFixtureEntry() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "forward-origin":
		for _, host := range []string{"203.0.113.10", "2001:db8:ffff::10"} {
			for _, port := range []int{18081, 18082} {
				listen := net.JoinHostPort(host, strconv.Itoa(port))
				tcp, err := net.Listen("tcp", listen)
				must(err)
				go func() {
					for {
						c, err := tcp.Accept()
						if err != nil {
							return
						}
						go func() {
							defer c.Close()
							peer := fmt.Sprintf("%s/%d", c.RemoteAddr(), port)
							fmt.Fprintln(c, peer)
							s := bufio.NewScanner(c)
							for s.Scan() {
								fmt.Fprintln(c, peer)
							}
							if s.Err() == nil {
								fmt.Fprintln(c, "EOF:"+peer)
							}
						}()
					}
				}()
				udp, err := net.ListenPacket("udp", listen)
				must(err)
				go func() {
					b := make([]byte, 2048)
					for {
						_, peer, err := udp.ReadFrom(b)
						if err != nil {
							return
						}
						_, _ = udp.WriteTo([]byte(fmt.Sprintf("%s/%d", peer, port)), peer)
					}
				}()
			}
		}
		select {}
	case "forward-client":
		dialer := net.Dialer{Timeout: 600 * time.Millisecond}
		if os.Args[4] != "" {
			if os.Args[2] == "tcp" {
				dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP(os.Args[4])}
			} else {
				dialer.LocalAddr = &net.UDPAddr{IP: net.ParseIP(os.Args[4])}
			}
		}
		c, err := dialer.Dial(os.Args[2], os.Args[3])
		enc := json.NewEncoder(os.Stdout)
		if err != nil {
			must(enc.Encode(forwardReply{Error: err.Error()}))
			return true
		}
		defer c.Close()
		reader := bufio.NewReader(c)
		receive := func() forwardReply {
			var value string
			var err error
			if os.Args[2] == "tcp" {
				value, err = reader.ReadString('\n')
			} else {
				b := make([]byte, 2048)
				var n int
				n, err = c.Read(b)
				value = string(b[:n])
			}
			if err != nil {
				return forwardReply{Error: err.Error()}
			}
			return forwardReply{Value: strings.TrimSpace(value)}
		}
		c.SetDeadline(time.Now().Add(600 * time.Millisecond))
		ready := forwardReply{Value: "ready"}
		if os.Args[2] == "tcp" {
			ready = receive()
		}
		must(enc.Encode(ready))
		if ready.Error != "" {
			return true
		}
		s := bufio.NewScanner(os.Stdin)
		for s.Scan() {
			c.SetDeadline(time.Now().Add(600 * time.Millisecond))
			if s.Text() == "halfclose" {
				err = c.(*net.TCPConn).CloseWrite()
			} else {
				_, err = io.WriteString(c, "probe\n")
			}
			if err != nil {
				must(enc.Encode(forwardReply{Error: err.Error()}))
			} else {
				must(enc.Encode(receive()))
			}
		}
		return true
	}
	return false
}

type forwardClient struct {
	cmd   *exec.Cmd
	input io.WriteCloser
	dec   *json.Decoder
	ready forwardReply
}

func newForwardClient(ctx context.Context, exe, network, address, source string) *forwardClient {
	c := &forwardClient{cmd: exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "forward-client", network, address, source)}
	var err error
	c.input, err = c.cmd.StdinPipe()
	must(err)
	output, err := c.cmd.StdoutPipe()
	must(err)
	c.cmd.Stderr = os.Stderr
	c.dec = json.NewDecoder(output)
	must(c.cmd.Start())
	must(c.dec.Decode(&c.ready))
	return c
}

func (c *forwardClient) close() {
	c.input.Close()
	_ = c.cmd.Wait()
}

func (c *forwardClient) request(command string) forwardReply {
	_, err := fmt.Fprintln(c.input, command)
	must(err)
	var reply forwardReply
	must(c.dec.Decode(&reply))
	return reply
}

func forwardOK(r forwardReply, prefix string) string {
	if r.Error != "" || !strings.HasPrefix(r.Value, prefix) {
		panic(fmt.Sprintf("forward expected %q, got %+v", prefix, r))
	}
	return r.Value
}

func forwardBlocked(r forwardReply) {
	if r.Error == "" {
		panic(fmt.Sprintf("blocked forward still passed traffic: %+v", r))
	}
}

func forwardFixtureConfig(targetPort int) map[string]any {
	var inbounds, outbounds, routes []any
	for i := 1; i <= 3; i++ {
		tag, listen, target, iface, mark := fmt.Sprintf("forward-%d", i), "192.0.2.1", "203.0.113.10", "wan1", uint32(0x45000000+i)
		if i == 2 {
			listen, target = "2001:db8:1::1", "2001:db8:ffff::10"
		}
		if i == 3 {
			tag, iface, mark = "node-1", "wan0", 0x43000001
		}
		inbounds = append(inbounds, map[string]any{"type": "direct", "tag": tag, "listen": listen, "listen_port": 21010 + i, "override_address": target, "override_port": targetPort, "udp_timeout": "2s"})
		outbounds = append(outbounds, map[string]any{"type": "direct", "tag": tag + "-out", "bind_interface": iface, "routing_mark": mark})
		// Set both advertised timeout surfaces. The experiment below must still
		// prove actual expiry; accepting either field is insufficient evidence.
		routes = append(routes, map[string]any{"inbound": []string{tag}, "action": "route", "outbound": tag + "-out", "udp_timeout": "2s"})
	}
	return map[string]any{"log": map[string]any{"level": "error"}, "inbounds": inbounds, "outbounds": outbounds, "route": map[string]any{"rules": routes}}
}

// Limits stay in stable rules while ACL sets change. Recreating these anonymous
// connlimits on each lease renewal would forget live admissions. Production must
// preserve this property in its shared root-owned transaction and watchdog.
func installForwardFixtureGuard() {
	var b strings.Builder
	b.WriteString("add table inet forward_probe\n")
	for _, direction := range []string{"input", "output"} {
		fmt.Fprintf(&b, "add chain inet forward_probe %s { type filter hook %s priority -150; policy accept; }\n", direction, direction)
	}
	b.WriteString("add chain inet forward_probe admission { type filter hook input priority -149; policy accept; }\n")
	b.WriteString("add set inet forward_probe blocked { type mark; }\n")
	b.WriteString("add set inet forward_probe target_ports { type inet_service; elements = { 18081 }; }\n")
	for i := 1; i <= 2; i++ {
		family, addrType := "ip", "ipv4_addr"
		if i == 2 {
			family, addrType = "ip6", "ipv6_addr"
		}
		mark, port := 0x45000000+i, 21010+i
		fmt.Fprintf(&b, "add set inet forward_probe f%d_sources { type %s; flags interval; }\n", i, addrType)
		fmt.Fprintf(&b, "add ct timeout inet forward_probe f%d_udp { protocol udp; l3proto %s; policy = { unreplied: 2, replied: 2 }; }\n", i, family)
		fmt.Fprintf(&b, "add rule inet forward_probe input ct direction original meta l4proto { tcp, udp } th dport %d ct mark set 0x%08x\n", port, mark)
		fmt.Fprintf(&b, "add counter inet forward_probe f%d_rx\nadd counter inet forward_probe f%d_tx\n", i, i)
	}
	b.WriteString("add rule inet forward_probe output meta mark & 0xff000000 == 0x45000000 ct mark set meta mark\n")
	for _, direction := range []string{"input", "output"} {
		fmt.Fprintf(&b, "add rule inet forward_probe %s ct mark @blocked drop\n", direction)
	}
	for i := 1; i <= 2; i++ {
		family, target, source := "ip", "203.0.113.10", "198.51.100.1"
		if i == 2 {
			family, target, source = "ip6", "2001:db8:ffff::10", "2001:db8:2::1"
		}
		mark, port := 0x45000000+i, 21010+i
		fmt.Fprintf(&b, "add rule inet forward_probe input ct mark 0x%08x ct direction original %s saddr != @f%d_sources drop\n", mark, family, i)
		fmt.Fprintf(&b, "add rule inet forward_probe output ct mark 0x%08x ct direction reply %s daddr != @f%d_sources drop\n", mark, family, i)
		fmt.Fprintf(&b, "add rule inet forward_probe input ct mark 0x%08x ct direction original iifname != \"wan0\" drop\n", mark)
		fmt.Fprintf(&b, "add rule inet forward_probe input ct mark 0x%08x ct direction original udp dport %d ct timeout set \"f%d_udp\"\n", mark, port, i)
		fmt.Fprintf(&b, "add rule inet forward_probe admission ct mark 0x%08x ct direction original ct state new tcp dport %d ct count over 2 drop\n", mark, port)
		fmt.Fprintf(&b, "add rule inet forward_probe admission ct mark 0x%08x ct direction original ct state new udp dport %d ct count over 2 drop\n", mark, port)
		fmt.Fprintf(&b, "add rule inet forward_probe output ct mark 0x%08x ct direction original oifname != \"wan1\" drop\n", mark)
		fmt.Fprintf(&b, "add rule inet forward_probe output ct mark 0x%08x ct direction original %s saddr != %s drop\n", mark, family, source)
		fmt.Fprintf(&b, "add rule inet forward_probe output ct mark 0x%08x ct direction original %s daddr != %s drop\n", mark, family, target)
		fmt.Fprintf(&b, "add rule inet forward_probe output ct mark 0x%08x ct direction original meta l4proto { tcp, udp } th dport != @target_ports drop\n", mark)
		for _, d := range []struct{ chain, suffix string }{{"input", "rx"}, {"output", "tx"}} {
			fmt.Fprintf(&b, "add rule inet forward_probe %s ct mark 0x%08x counter name f%d_%s return\n", d.chain, mark, i, d.suffix)
		}
	}
	for _, direction := range []string{"input", "output"} {
		fmt.Fprintf(&b, "add rule inet forward_probe %s ct mark & 0xff000000 == 0x45000000 drop\n", direction)
	}
	nftInput(b.String())
}

func forwardBytes(table, name string) uint64 {
	b, err := exec.Command("nft", "-j", "list", "counter", "inet", table, name).Output()
	must(err)
	var v struct {
		Nftables []struct {
			Counter struct{ Bytes uint64 } `json:"counter"`
		} `json:"nftables"`
	}
	must(json.Unmarshal(b, &v))
	var n uint64
	for _, entry := range v.Nftables {
		n += entry.Counter.Bytes
	}
	return n
}

func testPortForward(ctx context.Context, exe string) {
	var failures []string
	run("ip", "netns", "exec", "landing", "ip", "addr", "add", "192.0.2.3/24", "dev", "peer0")
	origin := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "forward-origin")
	origin.Stderr = os.Stderr
	must(origin.Start())
	defer func() { _ = origin.Process.Kill(); _ = origin.Wait() }()
	installForwardFixtureGuard()
	nodeRules, err := nft.NodeRules([]agentproto.NodeSpec{{NodeID: 1, Core: "singbox", ListenPort: 21013}})
	must(err)
	nftInput(nodeRules)
	stop := launchProduction(ctx, exe, "forward-prototype", "", forwardFixtureConfig(18081))
	defer func() { stop() }()
	time.Sleep(250 * time.Millisecond)

	c := newForwardClient(ctx, exe, "tcp", "192.0.2.1:21011", "")
	forwardBlocked(c.ready)
	c.close()
	nftInput("add element inet forward_probe f1_sources { 192.0.2.2/32 }\nadd element inet forward_probe f2_sources { 2001:db8:1::2/128 }\n")
	c = newForwardClient(ctx, exe, "tcp", "192.0.2.1:21011", "192.0.2.3")
	forwardBlocked(c.ready)
	c.close()
	fmt.Println("PASS forward empty and explicit source ACL")

	addresses := []string{"192.0.2.1:21011", "[2001:db8:1::1]:21012"}
	prefixes := []string{"198.51.100.1:", "[2001:db8:2::1]:"}
	var held []*forwardClient
	defer func() {
		for _, c := range held {
			c.close()
		}
	}()
	for i, addr := range addresses {
		tcp := newForwardClient(ctx, exe, "tcp", addr, "")
		forwardOK(tcp.ready, prefixes[i])
		forwardOK(tcp.request("probe"), prefixes[i])
		udp := newForwardClient(ctx, exe, "udp", addr, "")
		forwardOK(udp.ready, "ready")
		first := forwardOK(udp.request("probe"), prefixes[i])
		if next := forwardOK(udp.request("probe"), prefixes[i]); next != first {
			panic("active UDP mapping unexpectedly replaced")
		}
		held = append(held, tcp, udp)
	}
	if forwardBytes(nft.NodeTable, "n1_rx") != 0 || forwardBytes(nft.NodeTable, "n1_tx") != 0 {
		panic("forward traffic charged to node with same numeric ID")
	}
	for i := 1; i <= 2; i++ {
		if forwardBytes("forward_probe", fmt.Sprintf("f%d_rx", i)) == 0 || forwardBytes("forward_probe", fmt.Sprintf("f%d_tx", i)) == 0 {
			panic("forward ingress/egress not counted")
		}
	}
	fmt.Println("PASS forward fixed target TCP/UDP IPv4/IPv6, wan1 source and independent accounting")
	for _, mark := range []int{0x45000001, 0x45000003} {
		for _, network := range []string{"tcp", "udp"} {
			if err := markedFixtureDial(ctx, network, "203.0.113.10:18080", mark); err == nil {
				panic("forward escaped fixed destination or unknown identity")
			}
		}
	}
	fmt.Println("PASS forward denies different target port and unknown forward identity")
	conflictCtx, cancelConflict := context.WithTimeout(ctx, 2*time.Second)
	conflict := exec.CommandContext(conflictCtx, "setpriv", "--reuid=65534", "--regid=65534", "--clear-groups", "--bounding-set=-all,+net_bind_service,+net_raw", "--inh-caps=+net_bind_service,+net_raw", "--ambient-caps=+net_bind_service,+net_raw", exe, "launch", "/tmp/forward-prototype.json")
	conflictOutput, conflictErr := conflict.CombinedOutput()
	cancelConflict()
	if conflictErr == nil || !strings.Contains(string(conflictOutput), "address already in use") {
		panic(fmt.Sprintf("occupied listener was not rejected: %v %s", conflictErr, conflictOutput))
	}
	forwardOK(held[0].request("probe"), prefixes[0])
	fmt.Println("PASS forward occupied listener rejects second process without disrupting owner")

	second := newForwardClient(ctx, exe, "tcp", addresses[0], "")
	forwardOK(second.ready, prefixes[0])
	third := newForwardClient(ctx, exe, "tcp", addresses[0], "")
	forwardBlocked(third.ready)
	third.close()
	forwardOK(held[0].request("probe"), prefixes[0])
	half := second.request("halfclose")
	if half.Error != "" || !strings.HasPrefix(half.Value, "EOF:"+prefixes[0]) {
		failures = append(failures, "TCP half-close did not preserve the response direction")
		fmt.Println("FAIL forward TCP half-close")
	} else {
		fmt.Println("PASS forward TCP half-close")
	}
	second.close()
	fmt.Println("PASS forward TCP limit without breaking admitted connection")

	udp2 := newForwardClient(ctx, exe, "udp", addresses[0], "")
	forwardOK(held[1].request("probe"), prefixes[0])
	forwardOK(udp2.request("probe"), prefixes[0])
	udp3 := newForwardClient(ctx, exe, "udp", addresses[0], "")
	forwardBlocked(udp3.request("probe"))
	beforeIdle := forwardOK(held[1].request("probe"), prefixes[0])
	time.Sleep(5 * time.Second)
	afterIdle := forwardOK(held[1].request("probe"), prefixes[0])
	if beforeIdle == afterIdle {
		failures = append(failures, "UDP mapping did not expire after 5s with inbound and route timeout 2s")
		fmt.Println("FAIL forward UDP idle mapping expiry")
	} else {
		fmt.Println("PASS forward UDP idle mapping expiry")
	}
	forwardOK(udp3.request("probe"), prefixes[0])
	udp2.close()
	udp3.close()
	fmt.Println("PASS forward UDP conntrack limit and reclaimed admission capacity")

	nftInput("flush set inet forward_probe f1_sources\n")
	forwardBlocked(held[0].request("probe"))
	forwardBlocked(held[1].request("probe"))
	forwardOK(held[2].request("probe"), prefixes[1])
	forwardOK(held[3].request("probe"), prefixes[1])
	fmt.Println("PASS forward ACL revokes established TCP/UDP; other forward unaffected")
	nftInput("add element inet forward_probe blocked { 0x45000002 }\n")
	forwardBlocked(held[2].request("probe"))
	forwardBlocked(held[3].request("probe"))
	fmt.Println("PASS forward disable revokes established TCP/UDP")

	// A node sharing the numeric ID still works under forward blocking.
	peer := newForwardClient(ctx, exe, "tcp", "192.0.2.1:21013", "")
	forwardOK(peer.ready, "192.0.2.1:")
	peer.close()
	if forwardBytes(nft.NodeTable, "n1_rx") == 0 || forwardBytes(nft.NodeTable, "n1_tx") == 0 {
		panic("node accounting missing")
	}
	fmt.Println("PASS forward blocks do not affect same-ID node and its accounting")

	// Fence old generation before replacing the process. The production shared
	// transaction must also preserve actual-revision receipts on any failure.
	nftInput("add element inet forward_probe blocked { 0x45000001 }\n")
	stop()
	nftInput("flush set inet forward_probe target_ports\nadd element inet forward_probe target_ports { 18082 }\n")
	stop = launchProduction(ctx, exe, "forward-replaced", "", forwardFixtureConfig(18082))
	time.Sleep(250 * time.Millisecond)
	nftInput("add element inet forward_probe f1_sources { 192.0.2.2/32 }\nflush set inet forward_probe blocked\n")
	for i, c := range held {
		if i%2 == 0 {
			forwardBlocked(c.request("probe"))
		} else {
			value := forwardOK(c.request("probe"), prefixes[i/2])
			if !strings.HasSuffix(value, "/18082") {
				panic("UDP retained previous target generation")
			}
		}
	}
	fmt.Println("PASS forward target replacement closes old TCP and replaces UDP destination")
	if len(failures) != 0 {
		panic("forward qualification failed: " + strings.Join(failures, "; "))
	}
}
