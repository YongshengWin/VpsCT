//go:build linux

// Isolated sing-box network binding experiment; never shipped in production.
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
	"runtime"
	"strings"
	"time"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/proxysandbox"
	"golang.org/x/sys/unix"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func run(args ...string) {
	b, e := exec.Command(args[0], args[1:]...).CombinedOutput()
	if e != nil {
		panic(fmt.Sprintf("%v: %v %s", args, e, b))
	}
}
func main() {
	if handled, err := agentnet.Entry(os.Args[1:]); handled {
		must(err)
		return
	}
	if os.Getenv("CTLVPS_BIND_FIXTURE") != "1" {
		panic("explicit isolated fixture environment required")
	}
	if _, e := os.Stat("/.dockerenv"); e != nil {
		panic("container required")
	}
	if forwardFixtureEntry() {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "launch" {
		runtime.LockOSThread()
		must(proxysandbox.Install())
		must(unix.Exec("/fixture-sing-box", []string{"sing-box", "run", "-c", os.Args[2]}, os.Environ()))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "echo" {
		echo()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "client-probe" {
		clientProbe()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "held-flows" {
		flows := allFlows()
		defer closeFlows(flows)
		must(os.WriteFile("/tmp/fixture-flows-ready", []byte("ready"), 0600))
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat("/tmp/fixture-flows-check"); err == nil {
				blockedFlows(flows)
				must(os.WriteFile("/tmp/fixture-flows-blocked", []byte("blocked"), 0600))
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		panic("held-flow fixture not signaled")
	}
	if len(os.Args) == 3 && os.Args[1] == "bootstrap-probe" {
		mark := 0x44000101
		if os.Args[2] == "unmarked" {
			mark = 0
		}
		must(markedFixtureDial(context.Background(), "tcp", "198.51.100.2:15353", mark))
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "listener-probe" {
		c, err := net.DialTimeout("tcp", os.Args[2], 100*time.Millisecond)
		must(err)
		c.Close()
		return
	}
	test()
}
func echo() {
	serveDNS()
	addresses := []string{"203.0.113.10:18080", "[2001:db8:ffff::10]:18080"}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "socks-private" || os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-private" {
		addresses = append(addresses, "10.44.0.10:18080")
	}
	for _, address := range addresses {
		l, e := net.Listen("tcp", address)
		must(e)
		go func() {
			for {
				c, e := l.Accept()
				if e != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					_, _ = c.Write([]byte(c.RemoteAddr().(*net.TCPAddr).IP.String() + "\n"))
					s := bufio.NewScanner(c)
					for s.Scan() {
						_, _ = fmt.Fprintln(c, c.RemoteAddr().(*net.TCPAddr).IP.String())
					}
				}(c)
			}
		}()
		u, e := net.ListenPacket("udp", address)
		must(e)
		go func() {
			b := make([]byte, 2048)
			for {
				_, a, e := u.ReadFrom(b)
				if e != nil {
					return
				}
				_, _ = u.WriteTo([]byte(a.(*net.UDPAddr).IP.String()), a)
			}
		}()
	}
	select {}
}
func address(ip string, port int) []byte {
	p := net.ParseIP(ip)
	var b []byte
	if p == nil {
		if len(ip) > 255 {
			panic("oversized fixture domain")
		}
		b = append([]byte{3, byte(len(ip))}, []byte(ip)...)
	} else if v := p.To4(); v != nil {
		b = append([]byte{1}, v...)
	} else {
		b = append([]byte{4}, p.To16()...)
	}
	return append(b, byte(port>>8), byte(port))
}
func readAddress(r io.Reader) (string, int, error) {
	var kind [1]byte
	if _, e := io.ReadFull(r, kind[:]); e != nil {
		return "", 0, e
	}
	n := 4
	if kind[0] == 4 {
		n = 16
	} else if kind[0] != 1 {
		return "", 0, fmt.Errorf("unsupported reply address")
	}
	b := make([]byte, n+2)
	_, e := io.ReadFull(r, b)
	return net.IP(b[:n]).String(), int(binary.BigEndian.Uint16(b[n:])), e
}
func socks(command byte, target string) (net.Conn, string, int, error) {
	return socksAt(1080, command, target)
}
func socksAt(listen int, command byte, target string) (net.Conn, string, int, error) {
	c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listen), time.Second)
	if e != nil {
		return nil, "", 0, e
	}
	fail := func(e error) (net.Conn, string, int, error) { c.Close(); return nil, "", 0, e }
	c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	_, e = c.Write([]byte{5, 1, 0})
	if e != nil {
		return fail(e)
	}
	var greeting [2]byte
	if _, e = io.ReadFull(c, greeting[:]); e != nil {
		return fail(e)
	}
	if greeting != [2]byte{5, 0} {
		return fail(fmt.Errorf("SOCKS auth rejected"))
	}
	request := append([]byte{5, command, 0}, address(target, 18080)...)
	if command == 3 {
		request = append([]byte{5, 3, 0}, address("0.0.0.0", 0)...)
	}
	if _, e = c.Write(request); e != nil {
		return fail(e)
	}
	var reply [3]byte
	if _, e = io.ReadFull(c, reply[:]); e != nil {
		return fail(e)
	}
	if reply[1] != 0 {
		return fail(fmt.Errorf("SOCKS command rejected %d", reply[1]))
	}
	ip, port, e := readAddress(c)
	if e != nil {
		return fail(e)
	}
	return c, ip, port, nil
}
func tcp(target string) (string, error) {
	c, _, _, e := socks(1, target)
	if e != nil {
		return "", e
	}
	defer c.Close()
	b := make([]byte, 100)
	n, e := c.Read(b)
	return strings.TrimSpace(string(b[:n])), e
}
func udp(target string) (string, error) {
	return udpAt(1080, target)
}
func udpAt(listen int, target string) (string, error) {
	c, ip, port, e := socksAt(listen, 3, target)
	if e != nil {
		return "", e
	}
	defer c.Close()
	if net.ParseIP(ip).IsUnspecified() {
		ip = "127.0.0.1"
	}
	u, e := net.Dial("udp", net.JoinHostPort(ip, fmt.Sprint(port)))
	if e != nil {
		return "", e
	}
	defer u.Close()
	u.SetDeadline(time.Now().Add(700 * time.Millisecond))
	b := append([]byte{0, 0, 0}, address(target, 18080)...)
	b = append(b, []byte("fixture")...)
	if _, e = u.Write(b); e != nil {
		return "", e
	}
	b = make([]byte, 2048)
	n, e := u.Read(b)
	if e != nil {
		return "", e
	}
	if n < 10 {
		return "", fmt.Errorf("short UDP reply")
	}
	offset := 10
	if b[3] == 4 {
		offset = 22
	} else if b[3] == 3 {
		offset = 7 + int(b[4])
	} else if b[3] != 1 {
		return "", fmt.Errorf("unsupported UDP reply address")
	}
	if n < offset {
		return "", fmt.Errorf("short IPv6 reply")
	}
	return string(b[offset:n]), nil
}

func packets(name string) int64 {
	b, e := exec.Command("nft", "-j", "list", "counter", "inet", "bind_probe", name).Output()
	must(e)
	var stats struct {
		Nftables []struct {
			Counter struct {
				Packets int64 `json:"packets"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	must(json.Unmarshal(b, &stats))
	var count int64
	for _, n := range stats.Nftables {
		count += n.Counter.Packets
	}
	return count
}
func test() {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	run("ip", "netns", "add", "landing")
	defer run("ip", "netns", "del", "landing")
	for i := 0; i < 2; i++ {
		wan, peer := fmt.Sprintf("wan%d", i), fmt.Sprintf("peer%d", i)
		run("ip", "link", "add", wan, "type", "veth", "peer", "name", peer)
		run("ip", "link", "set", peer, "netns", "landing")
		prefix := "192.0.2"
		if i == 1 {
			prefix = "198.51.100"
		}
		run("ip", "addr", "add", prefix+".1/24", "dev", wan)
		run("ip", "-6", "addr", "add", fmt.Sprintf("2001:db8:%d::1/64", i+1), "dev", wan, "nodad")
		run("ip", "link", "set", wan, "up")
		run("ip", "netns", "exec", "landing", "ip", "addr", "add", prefix+".2/24", "dev", peer)
		run("ip", "netns", "exec", "landing", "ip", "-6", "addr", "add", fmt.Sprintf("2001:db8:%d::2/64", i+1), "dev", peer, "nodad")
		run("ip", "netns", "exec", "landing", "ip", "link", "set", peer, "up")
		run("ip", "route", "add", "203.0.113.10/32", "via", prefix+".2", "dev", wan, "metric", fmt.Sprint(100+i*100))
		run("ip", "-6", "route", "add", "2001:db8:ffff::10/128", "via", fmt.Sprintf("2001:db8:%d::2", i+1), "dev", wan, "metric", fmt.Sprint(100+i*100))
	}
	run("ip", "netns", "exec", "landing", "ip", "link", "set", "lo", "up")
	run("ip", "netns", "exec", "landing", "ip", "addr", "add", "203.0.113.10/32", "dev", "lo")
	run("ip", "netns", "exec", "landing", "ip", "-6", "addr", "add", "2001:db8:ffff::10/128", "dev", "lo", "nodad")
	exe, e := os.Executable()
	must(e)
	endpoint := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "echo")
	endpoint.Stderr = os.Stderr
	must(endpoint.Start())
	defer func() { _ = endpoint.Process.Kill(); _ = endpoint.Wait() }()
	run("nft", "add", "table", "inet", "bind_probe")
	run("nft", "add", "counter", "inet", "bind_probe", "wan0")
	run("nft", "add", "counter", "inet", "bind_probe", "wan1")
	run("nft", "add", "chain", "inet", "bind_probe", "output", "{ type filter hook output priority 0; policy accept; }")
	if os.Getenv("CTLVPS_BIND_CASE") == "socks" {
		testSOCKSEgress(ctx, exe)
		return
	}
	if os.Getenv("CTLVPS_BIND_CASE") == "socks-production" {
		testProductionSOCKSEgress(ctx, exe)
		return
	}
	if os.Getenv("CTLVPS_BIND_CASE") == "forward" {
		testPortForward(ctx, exe)
		return
	}
	if os.Getenv("CTLVPS_BIND_CASE") == "forward-tcp" {
		testCompiledForwardTCP(ctx, exe)
		return
	}
	cases := []struct {
		name, iface, src4, src6 string
		down, bad               bool
	}{
		{name: "default-route"},
		{name: "bind-secondary-interface", iface: "wan1"},
		{name: "bind-interface-and-sources", iface: "wan1", src4: "198.51.100.1", src6: "2001:db8:2::1"},
		{name: "source-address-only", src4: "198.51.100.1", src6: "2001:db8:2::1"},
		{name: "missing-interface", iface: "missing0", bad: true},
		{name: "unavailable-source", iface: "wan1", src4: "198.51.100.99", src6: "2001:db8:2::99", bad: true},
		{name: "interface-down", iface: "wan1", src4: "198.51.100.1", src6: "2001:db8:2::1", down: true, bad: true},
	}
	for index, tc := range cases {
		mark := uint32(0x43000001 + index)
		run("nft", "flush", "chain", "inet", "bind_probe", "output")
		run("nft", "reset", "counters", "table", "inet", "bind_probe")
		for _, iface := range []string{"wan0", "wan1"} {
			run("nft", "add", "rule", "inet", "bind_probe", "output", "meta", "mark", fmt.Sprint(mark), "oifname", fmt.Sprintf("%q", iface), "counter", "name", iface)
		}
		if tc.down {
			run("ip", "link", "set", "wan1", "down")
		}
		out := map[string]any{"type": "direct", "tag": "bound", "routing_mark": mark, "connect_timeout": "500ms"}
		if tc.iface != "" {
			out["bind_interface"] = tc.iface
		}
		if tc.src4 != "" {
			out["inet4_bind_address"] = tc.src4
		}
		if tc.src6 != "" {
			out["inet6_bind_address"] = tc.src6
		}
		cfg := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "socks", "listen": "127.0.0.1", "listen_port": 1080}}, "outbounds": []any{out}}
		data, e := json.Marshal(cfg)
		must(e)
		file := filepath.Join("/tmp", tc.name+".json")
		must(os.WriteFile(file, data, 0644))
		log, e := os.Create(filepath.Join("/tmp", tc.name+".log"))
		must(e)
		cmd := exec.CommandContext(ctx, "setpriv", "--reuid=65534", "--regid=65534", "--clear-groups", "--bounding-set=-all,+net_bind_service,+net_raw", "--inh-caps=+net_bind_service,+net_raw", "--ambient-caps=+net_bind_service,+net_raw", exe, "launch", file)
		cmd.Stdout, cmd.Stderr = log, log
		must(cmd.Start())
		ready := false
		for n := 0; n < 50; n++ {
			c, e := net.DialTimeout("tcp", "127.0.0.1:1080", 50*time.Millisecond)
			if e == nil {
				c.Close()
				ready = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !ready && !tc.bad {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			b, _ := os.ReadFile(log.Name())
			panic(string(b))
		}
		for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10"} {
			for _, probe := range []struct {
				name string
				f    func(string) (string, error)
			}{{"TCP", tcp}, {"UDP", udp}} {
				got, err := probe.f(target)
				if tc.bad {
					if err == nil {
						panic(fmt.Sprintf("%s %s %s bypassed: %s", tc.name, probe.name, target, got))
					}
				} else {
					expected := "192.0.2.1"
					if strings.Contains(target, ":") {
						expected = "2001:db8:1::1"
					}
					if tc.iface == "wan1" || tc.src4 != "" {
						expected = "198.51.100.1"
						if strings.Contains(target, ":") {
							expected = "2001:db8:2::1"
						}
					}
					if err != nil || got != expected {
						b, _ := os.ReadFile(log.Name())
						panic(fmt.Sprintf("%s %s %s got=%s want=%s err=%v log=%s", tc.name, probe.name, target, got, expected, err, b))
					}
				}
			}
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		log.Close()
		if !tc.bad {
			want, other := "wan0", "wan1"
			if tc.iface == "wan1" {
				want, other = other, want
			}
			if packets(want) == 0 || packets(other) != 0 {
				panic(fmt.Sprintf("%s used wrong interface or lost routing_mark: wan0=%d wan1=%d", tc.name, packets("wan0"), packets("wan1")))
			}
		} else if packets("wan0") != 0 || packets("wan1") != 0 {
			panic(fmt.Sprintf("%s emitted packets on a failed binding", tc.name))
		}
		fmt.Println("PASS", tc.name)
	}
	restoreWan1()
	testDNS(ctx, exe)
	testEstablished(ctx, exe)
	testLeaseGuard(ctx, exe)
	testProductionConfig(ctx, exe)
	fmt.Println("PASS routing_mark preserved under production seccomp and proxy capabilities")
}
