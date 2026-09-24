//go:build linux

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/nft"
	"ctlvps/internal/proxyguard"
	"golang.org/x/sys/unix"
)

func testProductionSOCKSEgress(ctx context.Context, exe string) {
	version, err := exec.Command("/fixture-sslocal", "--version").CombinedOutput()
	must(err)
	if !strings.Contains(string(version), "1.25.0") {
		panic("fixture requires verified official shadowsocks-rust 1.25.0")
	}
	dir, err := os.MkdirTemp("/tmp", "socks-production-")
	must(err)
	defer os.RemoveAll(dir)
	collector := netinventory.New(dir, "fixture-boot")
	defer collector.Close()
	snapshot := collector.Collect()
	ds := &agentproto.DesiredState{NetworkBindingVersion: 1, NetworkEgressVersion: 1, Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.12.14"}}}
	var bindings []networkguard.Binding
	for i := 0; i < 2; i++ {
		address := []string{"198.51.100.1", "192.0.2.1"}[i]
		upstream := []string{"198.51.100.2", "192.0.2.2"}[i]
		ifaceID := ""
		for _, iface := range snapshot.Interfaces {
			if iface.Name == fmt.Sprintf("wan%d", 1-i) {
				ifaceID = iface.ID
			}
		}
		policy := networkconfig.Node{ListenMode: "address", ListenAddress: address, ListenInterfaceID: ifaceID, AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}
		cfg := networkconfig.SOCKS5{Server: fmt.Sprintf("truncate.upstream%d.fixture.test", i), ServerPort: 11080 + i, Authentication: "password", UDP: true, Family: "dual", ConnectTimeoutSeconds: 1,
			DNS:   networkconfig.Resolver{Transport: "udp", Address: "203.0.113.10", Port: 15353},
			Outer: networkconfig.Direct{InterfaceID: ifaceID, Family: "ipv4", SourceIPv4: &networkconfig.Address{InterfaceID: ifaceID, Address: address}, DNS: networkconfig.Resolver{Transport: "udp", Address: upstream, Port: 15353}},
		}
		var credential [16]byte
		credential[0] = byte(i + 1)
		n := agentproto.NodeSpec{NodeID: int64(257 + i), Core: "singbox", Protocol: "ss", ListenPort: 21001 + i,
			Params:  map[string]any{"method": "2022-blake3-aes-128-gcm", "password": base64.StdEncoding.EncodeToString(credential[:])},
			Network: &agentproto.NodeNetworkSpec{Policy: policy, SOCKS5: &agentproto.SOCKS5Egress{Config: cfg, Credentials: networkconfig.SOCKS5Credentials{Username: "fixture", Password: "fixture-only-password"}}}}
		ds.Nodes = append(ds.Nodes, n)
	}
	// Accounting exists before root opens the bootstrap socket, as in Agent.apply.
	rules, err := nft.NodeRules(ds.Nodes)
	must(err)
	nftInput(rules)
	// DNS must work before business leases are granted, without granting them.
	must(proxyguard.Install(ctx, "add table inet ctlvps_egress\nadd chain inet ctlvps_egress output { type filter hook output priority -150; policy accept; }\n", nil))
	var pending []networkguard.Binding
	for _, n := range ds.Nodes {
		pending = append(pending, networkguard.Binding{NodeID: n.NodeID, Core: n.Core, ListenPort: n.ListenPort, Wanted: n.Network.Policy, Pending: true})
	}
	pendingPlan, err := networkguard.New(pending)
	must(err)
	must(proxyguard.InstallNetwork(ctx, pendingPlan))
	for i := range ds.Nodes {
		n := &ds.Nodes[i]
		for _, transport := range []string{"udp", "tcp"} {
			name := fmt.Sprintf("bootstrap%d_%s", i, transport)
			run("nft", "add", "counter", "inet", "bind_probe", name)
			run("nft", "add", "rule", "inet", "bind_probe", "output", "meta", "mark", fmt.Sprint(0x44000101+i), "oifname", fmt.Sprintf("\"wan%d\"", 1-i), "meta", "l4proto", transport, "th", "dport", "15353", "counter", "name", name)
		}
		resolved, err := netinventory.ResolveSOCKS5WithDNS(ctx, n.NodeID, n.Network.Policy, n.Network.SOCKS5.Config, collector.Collect(), false, time.Now())
		must(err)
		if resolved.SOCKS5.Address != []string{"198.51.100.2", "192.0.2.2"}[i] || resolved.SOCKS5.Host != n.Network.SOCKS5.Config.Server {
			panic("domain upstream pin mismatch")
		}
		for _, transport := range []string{"udp", "tcp"} {
			if packets(fmt.Sprintf("bootstrap%d_%s", i, transport)) == 0 {
				panic("bootstrap DNS did not use its own mark/interface or TCP retry")
			}
		}
		n.RuntimeNetwork = &resolved
		bindings = append(bindings, networkguard.Binding{NodeID: n.NodeID, Core: n.Core, ListenPort: n.ListenPort, Wanted: n.Network.Policy, Applied: resolved})
	}
	counts, err := nft.New().ReadNodes(ctx)
	must(err)
	if len(counts) != 2 {
		panic("missing bootstrap accounting nodes")
	}
	for _, count := range counts {
		if count.Rx == 0 || count.Tx == 0 {
			panic("bootstrap DNS bytes were not charged to their node")
		}
	}
	fmt.Println("PASS isolated bootstrap DNS on each NIC, UDP truncation/TCP retry and node RX/TX accounting before business starts")
	for _, mode := range []string{"unmarked", "marked"} {
		cmd := exec.CommandContext(ctx, "setpriv", "--reuid=65534", "--regid=65534", "--clear-groups", "--bounding-set=-all,+net_raw", "--inh-caps=+net_raw", "--ambient-caps=+net_raw", exe, "bootstrap-probe", mode)
		err := cmd.Run()
		if (mode == "unmarked") != (err == nil) {
			panic("proxy socket bootstrap mark ownership check failed: " + mode)
		}
	}
	ds.Nodes[0].Blocked = true
	rules, err = nft.NodeRules(ds.Nodes)
	must(err)
	nftInput(rules)
	for _, id := range []int64{ds.Nodes[0].NodeID, 999} {
		blockedCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		_, err := netinventory.LookupBootstrap(blockedCtx, id, "upstream0.fixture.test", "ipv4", *ds.Nodes[0].RuntimeNetwork.Direct)
		cancel()
		if err == nil {
			panic("blocked or unknown node sent bootstrap DNS")
		}
	}
	_, err = netinventory.LookupBootstrap(ctx, ds.Nodes[1].NodeID, "upstream1.fixture.test", "ipv4", *ds.Nodes[1].RuntimeNetwork.Direct)
	must(err)
	ds.Nodes[0].Blocked = false
	rules, err = nft.NodeRules(ds.Nodes)
	must(err)
	nftInput(rules)
	fmt.Println("PASS proxy cannot forge bootstrap mark; quota-blocked/unknown DNS is denied while peer continues")
	cfg, err := (&core.SingBox{Paths: core.Paths{LogDir: "/tmp"}}).BuildConfig(ds, ds.Nodes)
	must(err)
	cfg["log"] = map[string]any{"level": "error"} // fixture log destination only
	// Endpoint and lease rules are production-generated. The separate systemd
	// fixture remains responsible for general cgroup permission integration.
	plan, err := networkguard.New(bindings)
	must(err)
	must(proxyguard.InstallNetwork(ctx, plan))
	monitor := startMonitor(ctx, plan, collector)
	defer monitor.stop()
	monitor.wait(false)
	stopUpstream := launchProduction(ctx, exe, "socks-production-upstream", "landing", socksUpstreamConfig())
	defer stopUpstream()
	stopServer := launchProduction(ctx, exe, "socks-production-server", "", cfg)
	defer stopServer()
	// Process Start and the independent client's local listener do not prove
	// either remote service is ready. Only retry TCP listener readiness here;
	// every authenticated business probe below still gets a single attempt.
	for i, n := range ds.Nodes {
		waitProductionListener(ctx, exe, "", net.JoinHostPort(n.RuntimeNetwork.SOCKS5.Address, strconv.Itoa(11080+i)))
		waitProductionListener(ctx, exe, "landing", net.JoinHostPort(n.Network.Policy.ListenAddress, strconv.Itoa(n.ListenPort)))
	}
	for i, n := range ds.Nodes {
		client, err := json.Marshal(map[string]any{"server": n.Network.Policy.ListenAddress, "server_port": n.ListenPort, "method": n.Params["method"], "password": n.Params["password"], "local_address": "127.0.0.1", "local_port": 1080 + i, "mode": "tcp_and_udp"})
		must(err)
		path := fmt.Sprintf("/tmp/socks-production-client-%d.json", i)
		must(os.WriteFile(path, client, 0600))
		cmd := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", "/fixture-sslocal", "-c", path)
		cmd.Stderr = os.Stderr
		must(cmd.Start())
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		ready := false
		for j := 0; j < 50; j++ {
			if exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "client-probe", strconv.Itoa(1080+i), "ready").Run() == nil {
				ready = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !ready {
			panic("independent SS fixture client did not start")
		}
	}
	probe := func(i int, transport, target string) {
		got, err := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "client-probe", strconv.Itoa(1080+i), transport, target).CombinedOutput()
		want := []string{"198.51.100.2", "192.0.2.2"}[i]
		if strings.Contains(target, ":") {
			want = fmt.Sprintf("2001:db8:%d::2", 2-i)
		}
		if err != nil || string(got) != want {
			panic(fmt.Sprintf("production SOCKS node %d %s %s: got %q want %q err %v", i, transport, target, got, want, err))
		}
	}
	for i := 0; i < 2; i++ {
		for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10", "echo.fixture.test", "truncate.fixture.test"} {
			for _, transport := range []string{"tcp", "udp"} {
				probe(i, transport, target)
			}
		}
	}
	fmt.Println("PASS unchanged production SS-2022 BuildConfig through authenticated SOCKS5, two NICs, TCP/UDP IPv4/IPv6 and business DNS")
	// First prove each otherwise-forbidden destination is reachable by this
	// exact interface/source without the node mark, then assert the fence drops
	// the marked attempt. No target availability assumption masks a bypass.
	for _, address := range []string{"203.0.113.10:18080", "198.51.100.2:15353"} {
		if err := markedFixtureDial(ctx, "tcp", address, 0); err != nil {
			panic(fmt.Sprintf("negative control unreachable %s: %v", address, err))
		}
		if markedFixtureDial(ctx, "tcp", address, 0x43000101) == nil {
			panic("SOCKS endpoint fence allowed unrelated destination or port")
		}
	}
	must(markedFixtureDial(ctx, "udp", "203.0.113.10:18080", 0))
	if markedFixtureDial(ctx, "udp", "203.0.113.10:18080", 0x43000101) == nil {
		panic("SOCKS endpoint fence allowed unrelated UDP relay address")
	}
	fmt.Println("PASS marked direct TCP/UDP business and direct TCP DNS blocked by production endpoint fence")
	run("ip", "link", "set", "wan1", "down")
	monitor.wait(true)
	for _, transport := range []string{"tcp", "udp"} {
		if exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "client-probe", "1080", transport, "203.0.113.10").Run() == nil {
			panic("disconnected SOCKS node bypassed its interface")
		}
		probe(1, transport, "203.0.113.10")
	}
	fmt.Println("PASS unavailable SOCKS outer interface blocks only its node; peer stays usable")
}

func waitProductionListener(ctx context.Context, exe, namespace, address string) {
	for attempt := 0; attempt < 50; attempt++ {
		args := []string{exe, "listener-probe", address}
		if namespace != "" {
			args = append([]string{"ip", "netns", "exec", namespace}, args...)
		}
		if exec.CommandContext(ctx, args[0], args[1:]...).Run() == nil {
			return
		}
		if ctx.Err() != nil {
			panic(ctx.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}
	panic("production fixture listener did not become ready: " + address)
}

func markedFixtureDial(ctx context.Context, network, address string, mark int) error {
	var source net.Addr = &net.TCPAddr{IP: net.ParseIP("198.51.100.1")}
	if network == "udp" {
		source = &net.UDPAddr{IP: net.ParseIP("198.51.100.1")}
	}
	d := net.Dialer{Timeout: 500 * time.Millisecond, LocalAddr: source, Control: func(_, _ string, raw syscall.RawConn) error {
		var sockErr error
		if err := raw.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, "wan1")
			if sockErr == nil {
				sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
			}
		}); err != nil {
			return err
		}
		return sockErr
	}}
	c, err := d.DialContext(ctx, network, address)
	if err != nil {
		return err
	}
	defer c.Close()
	if network == "udp" {
		c.SetDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err = c.Write([]byte("fixture")); err != nil {
			return err
		}
		_, err = c.Read(make([]byte, 512))
	}
	return err
}
