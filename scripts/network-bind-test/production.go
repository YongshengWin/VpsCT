//go:build linux

package main

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/nft"
	"ctlvps/internal/proxyguard"
)

func clientProbe() {
	port, err := strconv.Atoi(os.Args[2])
	must(err)
	if os.Args[3] == "ready" {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		must(err)
		c.Close()
		return
	}
	if os.Args[3] == "udp" {
		got, err := udpAt(port, os.Args[4])
		must(err)
		fmt.Print(got)
		return
	}
	c, _, _, err := socksAt(port, 1, os.Args[4])
	must(err)
	defer c.Close()
	// Write a request, but the origin also sends an immediate greeting. This
	// deliberately keeps the early-response case: a client write alone does not
	// serialize the pinned sing-box client's internal handshake and response read.
	if os.Args[3] != "greeting" {
		_, err = c.Write([]byte("probe\n"))
		must(err)
	}
	got, err := bufio.NewReader(c).ReadString('\n')
	must(err)
	fmt.Print(strings.TrimSpace(got))
}

func launchProduction(ctx context.Context, exe, name, namespace string, cfg map[string]any) func() {
	data, err := json.Marshal(cfg)
	must(err)
	file := filepath.Join("/tmp", name+".json")
	must(os.WriteFile(file, data, 0644))
	run("/fixture-sing-box", "check", "-c", file)
	args := []string{"setpriv", "--reuid=65534", "--regid=65534", "--clear-groups", "--bounding-set=-all,+net_bind_service,+net_raw", "--inh-caps=+net_bind_service,+net_raw", "--ambient-caps=+net_bind_service,+net_raw", exe, "launch", file}
	if namespace != "" {
		args = append([]string{"ip", "netns", "exec", namespace}, args...)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	must(cmd.Start())
	return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
}

// This experiment uses unchanged production BuildConfig output as the server;
// SOCKS is only a fixture client in the other namespace. Listener iif checks,
// SS-2022 inbound handling, DNS and marked direct egress all execute together.
func testProductionConfig(ctx context.Context, exe string) {
	dir, err := os.MkdirTemp("/tmp", "production-binding-")
	must(err)
	defer os.RemoveAll(dir)
	collector := netinventory.New(dir, "fixture-boot")
	defer collector.Close()
	snapshot := collector.Collect()
	_, reference := compileNodes(snapshot, "udp", "dual")
	ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.12.14"}}}
	clientIn, clientOut, clientRules := []any{}, []any{}, []any{}
	var bindings []networkguard.Binding
	for i, ref := range reference.Bindings {
		address := "198.51.100.1"
		if i == 1 {
			address = "192.0.2.1"
		}
		policy := ref.Wanted
		policy.ListenMode, policy.ListenAddress, policy.ListenInterfaceID = "address", address, ref.Applied.Direct.Interface.ID
		direct := ref.Applied.Direct.Config
		resolved, err := netinventory.ResolveBinding(policy, &direct, snapshot, false, time.Now())
		must(err)
		var credential [16]byte
		credential[0] = byte(i + 1)
		node := agentproto.NodeSpec{NodeID: ref.NodeID, Core: "singbox", Protocol: "ss", ListenPort: 21001 + i,
			Params:  map[string]any{"method": "2022-blake3-aes-128-gcm", "password": base64.StdEncoding.EncodeToString(credential[:])},
			Network: &agentproto.NodeNetworkSpec{Policy: policy, Direct: &direct}, RuntimeNetwork: &resolved}
		ds.Nodes = append(ds.Nodes, node)
		bindings = append(bindings, networkguard.Binding{NodeID: node.NodeID, Core: node.Core, ListenPort: node.ListenPort, Wanted: policy, Applied: resolved})
		tag := fmt.Sprintf("fixture-client-%d", i)
		clientIn = append(clientIn, map[string]any{"type": "socks", "tag": tag, "listen": "127.0.0.1", "listen_port": 1080 + i})
		clientOut = append(clientOut, map[string]any{"type": "shadowsocks", "tag": tag, "server": address, "server_port": node.ListenPort, "method": node.Params["method"], "password": node.Params["password"]})
		clientRules = append(clientRules, map[string]any{"inbound": []string{tag}, "action": "route", "outbound": tag})
	}
	driver := &core.SingBox{Paths: core.Paths{LogDir: "/tmp"}}
	cfg, err := driver.BuildConfig(ds, ds.Nodes)
	must(err)
	// Use the fixture's stderr; no routes/outbounds/listeners are rewritten.
	cfg["log"] = map[string]any{"level": "error"}
	rules, err := nft.NodeRules(ds.Nodes)
	must(err)
	nftInput(rules)
	plan, err := networkguard.New(bindings)
	must(err)
	must(proxyguard.InstallNetwork(ctx, plan))
	monitor := startMonitor(ctx, plan, collector)
	defer func() {
		if monitor != nil {
			monitor.stop()
		}
	}()
	monitor.wait(false)
	stopServer := launchProduction(ctx, exe, "production-server", "", cfg)
	defer stopServer()
	client := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": clientIn, "outbounds": clientOut, "route": map[string]any{"rules": clientRules}}
	stopClient := launchProduction(ctx, exe, "production-client", "landing", client)
	defer stopClient()
	ready := false
	for i := 0; i < 50; i++ {
		if exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "client-probe", "1080", "ready").Run() == nil {
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		panic("production client did not become ready")
	}
	for i := 0; i < 2; i++ {
		for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10", "binding.test", "truncate.binding.test"} {
			for _, transport := range []string{"tcp", "udp"} {
				got, err := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "client-probe", strconv.Itoa(1080+i), transport, target).CombinedOutput()
				want := "198.51.100.1"
				if i == 1 {
					want = "192.0.2.1"
				}
				if strings.Contains(target, ":") {
					want = fmt.Sprintf("2001:db8:%d::1", 2-i)
				}
				if err != nil || string(got) != want {
					panic(fmt.Sprintf("production node%d %s %s got=%s err=%v", i, transport, target, got, err))
				}
			}
		}
	}
	fmt.Println("PASS production BuildConfig SS-2022 listeners, per-node DNS and TCP/UDP IPv4/IPv6 egress")
	monitor.stop()
	pending := append([]networkguard.Binding(nil), bindings...)
	pending[0].Pending, pending[0].Applied = true, networkconfig.Resolved{}
	staged, err := networkguard.New(pending)
	must(err)
	must(proxyguard.InstallNetwork(ctx, staged))
	monitor = startMonitor(ctx, staged, collector)
	monitor.wait(true)
	for _, transport := range []string{"tcp", "udp"} {
		if exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "client-probe", "1080", transport, "203.0.113.10").Run() == nil {
			panic("pending production node obtained a lease")
		}
		got, err := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "client-probe", "1081", transport, "203.0.113.10").CombinedOutput()
		if err != nil || string(got) != "192.0.2.1" {
			panic(fmt.Sprintf("staged peer failed: %s %v", got, err))
		}
	}
	fmt.Println("PASS pending application stays blocked with a healthy NIC while its applied peer continues")
	// The official parser must accept the same bound Dial Fields in Reality's
	// handshake object. Complete authenticated Reality flow is a separate test.
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	must(err)
	reality := ds.Nodes[0]
	reality.Protocol = "vless"
	reality.Params = map[string]any{"uuid": "00000000-0000-4000-8000-000000000001", "handshake_server": "binding.test", "reality_private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "reality_short_id": "abcdef01"}
	cfg, err = driver.BuildConfig(ds, []agentproto.NodeSpec{reality})
	must(err)
	b, err := json.Marshal(cfg)
	must(err)
	file := filepath.Join("/tmp", "production-reality-check.json")
	must(os.WriteFile(file, b, 0644))
	run("/fixture-sing-box", "check", "-c", file)
	fmt.Println("PASS official parser accepts production bound Reality handshake configuration")
	monitor.stop()
	monitor = nil
	for i := range pending {
		pending[i].Pending, pending[i].Applied = true, networkconfig.Resolved{}
	}
	aborted, err := networkguard.New(pending)
	must(err)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if proxyguard.InstallNetwork(cancelled, aborted) == nil {
		panic("cancelled nft application succeeded")
	}
	persisted, err := proxyguard.LoadNetwork(ctx)
	must(err)
	if persisted == nil || persisted.Token != aborted.Token {
		panic("failed preflight lost restrictive intent")
	}
	must(proxyguard.Check(ctx))
	bad, err := proxyguard.RefreshNetwork(ctx, aborted.Token, collector.Collect())
	must(err)
	if len(bad) != 2 {
		panic("failed application's pending plan became healthy")
	}
	fmt.Println("PASS cancelled firewall preflight retains restrictive intent for watchdog recovery")
}
