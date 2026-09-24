//go:build linux

// Runs the actual controller API, agent executable, isolated network helper,
// systemd drivers and official sing-box in a disposable offline container.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	cryptotls "crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"ctlvps/internal/agent"
	"ctlvps/internal/api"
	"ctlvps/internal/auth"
	"ctlvps/internal/connlog"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/maintenance"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/proxyguard"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"ctlvps/internal/traffic"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func run(args ...string) string {
	b, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("fixture command %s failed: %v %s", args[0], err, b))
	}
	return strings.TrimSpace(string(b))
}
func writeJSON(path string, v any) {
	b, err := json.Marshal(v)
	must(err)
	must(os.WriteFile(path, b, 0600))
}
func eventually(label string, f func() bool) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	panic("timed out: " + label)
}
func copyFile(src, dst string, mode os.FileMode) {
	b, err := os.ReadFile(src)
	must(err)
	must(os.MkdirAll(filepath.Dir(dst), 0755))
	must(os.WriteFile(dst, b, mode))
}
func setupNetwork() {
	run("ip", "netns", "add", "landing")
	for i, prefix := range []string{"192.0.2", "198.51.100"} {
		wan, peer := fmt.Sprint("wan", i), fmt.Sprint("peer", i)
		run("ip", "link", "add", wan, "type", "veth", "peer", "name", peer)
		run("ip", "link", "set", peer, "netns", "landing")
		run("ip", "addr", "add", prefix+".1/24", "dev", wan)
		run("ip", "-6", "addr", "add", fmt.Sprintf("2001:db8:%d::1/64", i+1), "dev", wan, "nodad")
		run("ip", "link", "set", wan, "up")
		run("ip", "netns", "exec", "landing", "ip", "addr", "add", prefix+".2/24", "dev", peer)
		run("ip", "netns", "exec", "landing", "ip", "-6", "addr", "add", fmt.Sprintf("2001:db8:%d::2/64", i+1), "dev", peer, "nodad")
		run("ip", "netns", "exec", "landing", "ip", "link", "set", peer, "up")
		run("ip", "route", "add", "203.0.113.10/32", "via", prefix+".2", "dev", wan, "metric", fmt.Sprint(100+i*100))
		run("ip", "-6", "route", "add", "2001:db8:ffff::10/128", "via", fmt.Sprintf("2001:db8:%d::2", i+1), "dev", wan, "metric", fmt.Sprint(100+i*100))
		run("ip", "route", "add", "default", "via", prefix+".2", "dev", wan, "metric", fmt.Sprint(100+i*100))
		run("ip", "-6", "route", "add", "default", "via", fmt.Sprintf("2001:db8:%d::2", i+1), "dev", wan, "metric", fmt.Sprint(100+i*100))
	}
	run("ip", "netns", "exec", "landing", "ip", "link", "set", "lo", "up")
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "socks-private" || os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-private" {
		run("ip", "addr", "add", "10.23.0.1/24", "dev", "wan0")
		for _, address := range []string{"10.23.0.2/24", "10.23.0.3/24"} {
			run("ip", "netns", "exec", "landing", "ip", "addr", "add", address, "dev", "peer0")
		}
		run("ip", "netns", "exec", "landing", "ip", "addr", "add", "10.44.0.10/32", "dev", "lo")
		run("ip", "netns", "exec", "landing", "sysctl", "-w", "net.ipv4.ip_local_port_range=40000 60000")
	}
	run("ip", "netns", "exec", "landing", "ip", "addr", "add", "203.0.113.10/32", "dev", "lo")
	run("ip", "netns", "exec", "landing", "ip", "-6", "addr", "add", "2001:db8:ffff::10/128", "dev", "lo", "nodad")
	run("ip", "netns", "exec", "landing", "ip", "route", "add", "default", "via", "192.0.2.1", "dev", "peer0")
	run("ip", "netns", "exec", "landing", "ip", "-6", "route", "add", "default", "via", "2001:db8:1::1", "dev", "peer0")
	// Exercise native default-deny ingress as on a managed host. Only the
	// real agent may add its tagged node allowances; the guard remains earlier.
	run("nft", "add", "table", "inet", "filter")
	run("nft", "add", "chain", "inet", "filter", "input", "{ type filter hook input priority 0; policy drop; }")
	run("nft", "add", "rule", "inet", "filter", "input", "iifname", "lo", "accept")
	run("nft", "add", "rule", "inet", "filter", "input", "meta", "l4proto", "ipv6-icmp", "accept")
	run("nft", "add", "rule", "inet", "filter", "input", "ct", "state", "established,related", "accept")
}
func process(ctx context.Context, args ...string) func() {
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	must(c.Start())
	return func() { _ = c.Process.Kill(); _ = c.Wait() }
}
func probe(i int, transport, target string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", "/fixtures/probe", "client-probe", fmt.Sprint(1080+i), transport, target).CombinedOutput()
	return strings.TrimSpace(string(b)), err
}
func dumpNetworkFailure() {
	// Capture evidence before deferred shutdown revokes leases or stops clients.
	// Only isolated fixture logs/rules are read; never print generated credentials.
	for _, args := range [][]string{
		{"tail", "-n", "30", "/var/log/ctlvps/sing-box.log"},
		{"nft", "-j", "list", "table", "inet", "ctlvps_network"},
		{"systemctl", "show", "ctlvps-singbox.service", "-p", "InvocationID", "-p", "ActiveState"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		b, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		cancel()
		if len(b) > 32768 {
			b = b[len(b)-32768:]
		}
		fmt.Fprintf(os.Stderr, "fixture failure evidence %s: %v\n%s\n", args[0], err, b)
	}
}
func verify(i int) {
	for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10", "binding.test", "truncate.binding.test"} {
		want := []string{"192.0.2.1", "198.51.100.1"}[i]
		if strings.Contains(target, ":") {
			want = fmt.Sprintf("2001:db8:%d::1", i+1)
		}
		for _, transport := range []string{"tcp", "greeting", "udp"} {
			got, err := probe(i, transport, target)
			if err != nil || got != want {
				dumpNetworkFailure()
				panic(fmt.Sprintf("node %d %s %s: got %q, want %q, err %v", i, transport, target, got, want, err))
			}
		}
	}
}

// Keep the original official sing-box client path available to expose its
// SS2022 early-response failure. The independent client validates our unchanged
// server and lifecycle; it does not qualify sing-box's SS2022 outbound.
func startClient(ctx context.Context, i int, n domain.Node) func() {
	out, ok := subscription.SingBoxOutbound(proxynode.FromDomain(n), "")
	if !ok {
		panic("missing client renderer")
	}
	path := fmt.Sprintf("/tmp/network-client-%d.json", i)
	if os.Getenv("NETWORK_TEST_CLIENT") == "shadowsocks-rust" {
		if version := run("/fixtures/sslocal", "--version"); !strings.Contains(version, "1.25.0") {
			panic("fixture needs verified official shadowsocks-rust 1.25.0")
		}
		writeJSON(path, map[string]any{
			"server": out["server"], "server_port": out["server_port"], "method": out["method"], "password": out["password"],
			"local_address": "127.0.0.1", "local_port": 1080 + i, "mode": "tcp_and_udp",
		})
		return process(ctx, "ip", "netns", "exec", "landing", "/fixtures/sslocal", "-c", path)
	}
	writeJSON(path, map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "socks", "listen": "127.0.0.1", "listen_port": 1080 + i}}, "outbounds": []any{out}})
	return process(ctx, "ip", "netns", "exec", "landing", "/opt/ctlvps/bin/sing-box", "run", "-c", path)
}

func main() {
	if os.Getenv("CTLVPS_SYSTEMD_NETWORK_FIXTURE") != "1" || os.Geteuid() != 0 {
		panic("explicit disposable root fixture required")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		panic("container required")
	}
	if len(os.Args) == 2 && os.Args[1] == "ssh-peer" {
		sshFixturePeer()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "transit-landing" {
		transitLanding()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	setupNetwork()
	stopOrigin := process(ctx, "ip", "netns", "exec", "landing", "/fixtures/probe", "echo")
	defer stopOrigin()
	eventually("fixture origin and IPv6 neighbor discovery", func() bool {
		c, err := net.DialTimeout("tcp", "[2001:db8:ffff::10]:18080", time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	})
	copyFile("/fixtures/ctlvps-agent", "/usr/local/bin/ctlvps-agent", 0755)
	copyFile("/fixtures/sing-box", "/opt/ctlvps/bin/sing-box", 0755)
	if version := run("/opt/ctlvps/bin/sing-box", "version"); !strings.Contains(version, "sing-box version 1.14.1") {
		panic("fixture needs verified official sing-box 1.14.1")
	}
	b, err := os.ReadFile("/opt/ctlvps/bin/sing-box")
	must(err)
	sum := sha256.Sum256(b)
	writeJSON("/opt/ctlvps/bin/sing-box.trusted", map[string]string{"Version": corecompat.NetworkBaseline, "SHA256": hex.EncodeToString(sum[:])})
	must(os.MkdirAll("/etc/ctlvps", 0750))
	dir := "/var/lib/ctlvps-network-fixture"
	must(os.MkdirAll(dir, 0700))
	st, err := store.Open(filepath.Join(dir, "data.db"))
	must(err)
	defer st.Close()
	logs, err := connlog.Open(filepath.Join(dir, "connlog.db"))
	must(err)
	defer logs.Close()
	d := desired.New(st)
	must(st.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: corecompat.NetworkBaseline}))
	subs := subscription.NewService(st)
	shares := share.New(st, d)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	a := api.New(api.Deps{Store: st, Connlog: logs, Subs: subs, Desired: d, Shares: shares, Traffic: traffic.New(st), Logger: logger, Config: api.Config{DataDir: dir, Version: "isolated-fixture", StartedAt: time.Now(), SetupToken: "isolated-fixture-setup-only"}})
	var offline atomic.Bool
	var desiredRequests atomic.Uint64
	failedHeartbeat := make(chan struct{}, 1)
	handler := a.Handler()
	tls := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			if r.URL.Path == "/api/agent/v1/heartbeat" {
				select {
				case failedHeartbeat <- struct{}{}:
				default:
				}
			}
			w.WriteHeader(503)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/agent/v1/desired" {
			desiredRequests.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "transit" || os.Getenv("NETWORK_SYSTEMD_CASE") == "transit-ss2022" {
		run("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-keyout", "/tmp/transit-ca.key", "-out", "/tmp/transit-ca.crt", "-days", "1", "-subj", "/CN=isolated-fixture", "-addext", "subjectAltName=IP:127.0.0.1,IP:198.18.37.2")
		cert, e := cryptotls.LoadX509KeyPair("/tmp/transit-ca.crt", "/tmp/transit-ca.key")
		must(e)
		tls.TLS = &cryptotls.Config{Certificates: []cryptotls.Certificate{cert}}
		tls.Listener.Close()
		tls.Listener, e = net.Listen("tcp", "198.18.37.2:18443")
		must(e)
		run("nft", "add", "rule", "inet", "filter", "input", "iifname", "eth0", "tcp", "dport", "18443", "accept")
	}
	tls.StartTLS()
	defer tls.Close()
	must(os.WriteFile("/usr/local/share/ca-certificates/ctlvps-network-fixture.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tls.Certificate().Raw}), 0644))
	run("update-ca-certificates")
	server := domain.Server{Name: "network fixture", PublicHost: "192.0.2.1", CoreMode: domain.CoreModeStable, Enabled: true}
	must(st.CreateServer(ctx, &server))
	must(st.SetSetting(ctx, domain.SettingConnlogSelf, "false"))
	token := auth.RandomToken(24)
	must(st.SetAgentEnrollToken(ctx, server.ID, auth.HashToken(token), time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)))
	run("/usr/local/bin/ctlvps-agent", "enroll", "--server", tls.URL, "--token", token)
	state, err := agent.LoadState("/var/lib/ctlvps-agent")
	must(err)
	state.PollIntervalSec = 10
	must(state.Save("/var/lib/ctlvps-agent"))
	startFixtureAgent()
	defer run("systemctl", "stop", "ctlvps-agent.service")
	interfaces := map[string]string{}
	eventually("real agent inventory", func() bool {
		view, err := st.Network(ctx, server.ID)
		if err != nil || view.Snapshot == nil || view.Snapshot.Status != "ok" {
			return false
		}
		for _, nic := range view.Snapshot.Interfaces {
			interfaces[nic.Name] = nic.ID
		}
		return interfaces["wan0"] != "" && interfaces["wan1"] != ""
	})
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "transit" || os.Getenv("NETWORK_SYSTEMD_CASE") == "transit-ss2022" {
		testManagedTransit(ctx, st, d, shares, server, interfaces, newFixtureAdmin(ctx, tls), tls)
		return
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "native-subscriptions" {
		testNativeSubscriptions(ctx, st, d, shares, server, newFixtureAdmin(ctx, tls), tls)
		return
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "subscriptions" {
		testSubscriptionClients(ctx, st, d, shares, server, newFixtureAdmin(ctx, tls), tls)
		return
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "protocols" || os.Getenv("NETWORK_SYSTEMD_CASE") == "reality" {
		testProtocolBindings(ctx, st, d, server, interfaces, newFixtureAdmin(ctx, tls))
		return
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "wireguard" {
		testWireGuardSystemd(ctx, st, d, shares, server, interfaces, newFixtureAdmin(ctx, tls))
		return
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "wgaccess" {
		testWGAccess(ctx, st, d, server, newFixtureAdmin(ctx, tls))
		return
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "listen" {
		testStandaloneListen(ctx, st, d, server, interfaces, newFixtureAdmin(ctx, tls))
		return
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "mita" {
		testMitaSystemd(ctx, st, d, server, newFixtureAdmin(ctx, tls))
		return
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "ssh" {
		testSSHSystemd(ctx, st, d, shares, server, interfaces, newFixtureAdmin(ctx, tls))
		return
	}
	if strings.HasPrefix(os.Getenv("NETWORK_SYSTEMD_CASE"), "forward") {
		testForwardSystemd(ctx, st, d, server, interfaces, newFixtureAdmin(ctx, tls))
		return
	}
	if strings.HasPrefix(os.Getenv("NETWORK_SYSTEMD_CASE"), "socks") {
		testSOCKSSystemd(ctx, st, d, shares, server, interfaces, &offline, failedHeartbeat, newFixtureAdmin(ctx, tls))
		return
	}
	var nodes []domain.Node
	var allotments []domain.Share
	var lastOperation string
	for i, host := range []string{"192.0.2.1", "198.51.100.1"} {
		sh := domain.Share{Name: fmt.Sprint("fixture ", i), Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
		_, err := shares.Create(ctx, &sh)
		must(err)
		allotments = append(allotments, sh)
		list, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
		must(err)
		if len(list) != 1 {
			panic("missing share node")
		}
		profile := domain.EgressProfile{ServerID: server.ID, Name: fmt.Sprint("direct ", i), Kind: "direct", Enabled: true}
		config, _ := json.Marshal(networkconfig.Direct{InterfaceID: interfaces[fmt.Sprint("wan", i)], Family: "dual", DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.10", Port: 15353}})
		must(st.CreateEgressProfile(ctx, &profile, config))
		policy := &networkconfig.Node{ListenMode: "address", ListenAddress: host, ListenInterfaceID: interfaces[fmt.Sprint("wan", i)], AdvertiseMode: "override", OnUnavailable: "block", EgressProfileID: profile.ID, EgressRevision: 1}
		lastOperation = fmt.Sprintf("%032x", i+1)
		_, err = st.RequestReadyNodeNetwork(ctx, store.NodeNetworkRequest{ID: lastOperation, NodeID: list[0].ID, Network: policy, AdvertiseHost: &host}, domain.AuditEvent{})
		must(err)
		n, err := st.GetNode(ctx, list[0].ID)
		must(err)
		nodes = append(nodes, n)
	}
	waitApplied := func(rev int64) {
		eventually("applied revision", func() bool {
			ag, err := st.GetAgentByServer(ctx, server.ID)
			return err == nil && ag.AppliedRevision == rev && ag.ApplyError == ""
		})
	}
	applyQueued := func() {
		must(d.ReconcileNetworkOperations(ctx))
		rec, err := st.LatestDesiredState(ctx, server.ID)
		must(err)
		waitApplied(rec.Revision)
		eventually("network operation exact applied receipt", func() bool {
			op, err := st.NetworkOperation(ctx, lastOperation)
			return err == nil && op.Status == "applied" && op.DesiredRevision == rec.Revision && op.DesiredHash == rec.Hash
		})
	}
	applyQueued()
	fmt.Println("PASS durable network operation -> outbox publication -> exact real-agent applied receipt")
	fmt.Println("Testing SS-2022 with client:", os.Getenv("NETWORK_TEST_CLIENT"))
	for i, n := range nodes {
		stop := startClient(ctx, i, n)
		defer stop()
		eventually("client ready", func() bool { _, err := probe(i, "ready", ""); return err == nil })
		verify(i)
	}
	fmt.Println("PASS controller store -> HTTPS/isolated helper -> agent -> systemd -> SS-2022 TCP/UDP IPv4/IPv6 and DNS")
	eventually("real counters reach both shares", func() bool {
		for _, sh := range allotments {
			got, err := st.GetShare(ctx, sh.ID)
			if err != nil || got.UsedUpload == 0 || got.UsedDownload == 0 {
				return false
			}
		}
		return true
	})
	fmt.Println("PASS both node counters reach independent share ledgers through real heartbeats")
	// Hold the actual root target lock across multiple agent polls, longer than
	// a network lease. The accepted new intent must wait without stopping the
	// independent watcher which keeps the existing verified path usable.
	var releaseConfiguration func()
	eventually("configuration target available", func() bool {
		var err error
		releaseConfiguration, err = maintenance.NewManager().ConfigurationLock()
		if err != nil && !errors.Is(err, maintenance.ErrBusy) {
			panic(err)
		}
		return err == nil
	})
	defer releaseConfiguration()
	type waitingWorker struct {
		done   chan error
		stderr bytes.Buffer
		want   string
	}
	var workers []*waitingWorker
	for _, spec := range []struct{ operation, input, want string }{
		{"core-install", `{"BinDir":"/opt/ctlvps/bin","Name":"sing-box","Version":{}}`, "no version pinned"},
		{"agent-update", `{}`, "incomplete update spec"},
	} {
		w := &waitingWorker{done: make(chan error, 1), want: spec.want}
		cmd := exec.CommandContext(ctx, "/usr/local/bin/ctlvps-agent", spec.operation)
		cmd.Stdin = strings.NewReader(spec.input)
		cmd.Stderr = &w.stderr
		must(cmd.Start())
		defer cmd.Process.Kill()
		go func() { w.done <- cmd.Wait() }()
		workers = append(workers, w)
	}
	agentBefore, err := st.GetAgentByServer(ctx, server.ID)
	must(err)
	lastOperation = fmt.Sprintf("%032x", 5)
	_, err = st.RequestReadyNodeNetwork(ctx, store.NodeNetworkRequest{ID: lastOperation, NodeID: nodes[0].ID, ExpectedRevision: nodes[0].NetworkRevision, Network: nodes[0].Network}, domain.AuditEvent{})
	must(err)
	nodes[0], err = st.GetNode(ctx, nodes[0].ID)
	must(err)
	must(d.ReconcileNetworkOperations(ctx))
	deferred, err := st.LatestDesiredState(ctx, server.ID)
	must(err)
	if deferred.Revision <= agentBefore.AppliedRevision {
		panic("lock fixture did not publish a new desired revision")
	}
	requestsBefore := desiredRequests.Load()
	eventually("agent polls while configuration target is held", func() bool {
		ag, err := st.GetAgentByServer(ctx, server.ID)
		return err == nil && ag.LastSeenAt != nil && agentBefore.LastSeenAt != nil && ag.LastSeenAt.After(*agentBefore.LastSeenAt) && desiredRequests.Load() >= requestsBefore+2
	})
	agentDuring, err := st.GetAgentByServer(ctx, server.ID)
	must(err)
	pendingOperation, err := st.NetworkOperation(ctx, lastOperation)
	must(err)
	if agentDuring.AppliedRevision != agentBefore.AppliedRevision || pendingOperation.Status != "waiting_agent" {
		panic("configuration applied while another process held the agent target")
	}
	for _, w := range workers {
		select {
		case <-w.done:
			panic("resource worker entered installation before the coordinator released its target")
		default:
		}
	}
	verify(0)
	verify(1)
	releaseConfiguration()
	for _, w := range workers {
		select {
		case err := <-w.done:
			if err == nil || !strings.Contains(w.stderr.String(), w.want) {
				panic("resource worker did not continue to its bounded preflight after release")
			}
		case <-time.After(10 * time.Second):
			panic("resource worker did not resume after configuration lock release")
		}
	}
	applyQueued()
	verify(0)
	verify(1)
	fmt.Println("PASS real agent and resource workers defer across target lock; leases keep traffic usable; release resumes workers and queued intent")
	// A new profile default points at the other NIC, but the existing consumer
	// must keep its pinned revision through a real disable and resume cycle.
	profile, err := st.GetEgressProfile(ctx, nodes[0].Network.EgressProfileID)
	must(err)
	newDefault, _ := json.Marshal(networkconfig.Direct{InterfaceID: interfaces["wan1"], Family: "dual", DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.10", Port: 15353}})
	edit := store.EgressProfileRequest{ID: fmt.Sprintf("%032x", 6), Action: "update", ProfileID: profile.ID, ExpectedRevision: profile.CurrentRevision, Name: profile.Name, Kind: "direct", Enabled: true, Config: newDefault}
	saved, err := st.RequestEgressProfile(ctx, edit, domain.AuditEvent{})
	must(err)
	if saved.Status != "saved" || saved.Generation != 0 {
		panic("new profile default incorrectly queued a live migration")
	}
	verify(0)
	edit.ID, edit.ExpectedRevision, edit.Enabled = fmt.Sprintf("%032x", 7), saved.ResourceRevision, false
	disabled, err := st.RequestEgressProfile(ctx, edit, domain.AuditEvent{})
	must(err)
	lastOperation = disabled.ID
	applyQueued()
	for _, transport := range []string{"tcp", "udp"} {
		if _, err := probe(0, transport, "203.0.113.10"); err == nil {
			panic("disabled egress profile left consumer traffic usable")
		}
	}
	verify(1)
	edit.ID, edit.ExpectedRevision, edit.Enabled = fmt.Sprintf("%032x", 8), disabled.ResourceRevision, true
	resumed, err := st.RequestEgressProfile(ctx, edit, domain.AuditEvent{})
	must(err)
	lastOperation = resumed.ID
	applyQueued()
	verify(0)
	verify(1)
	unchanged, err := st.GetNode(ctx, nodes[0].ID)
	must(err)
	if unchanged.Network.EgressRevision != nodes[0].Network.EgressRevision {
		panic("egress resume silently upgraded existing consumer")
	}
	fmt.Println("PASS egress default stays pinned; disable blocks TCP/UDP; exact resume restores original NIC and keeps peer working")
	offline.Store(true)
	select {
	case <-failedHeartbeat:
	case <-time.After(45 * time.Second):
		panic("agent did not observe controller outage")
	}
	verify(0)
	verify(1)
	run("ip", "link", "set", "wan0", "down")
	eventually("offline guard blocks missing NIC", func() bool { _, err := probe(0, "tcp", "203.0.113.10"); return err != nil })
	verify(1)
	run("ip", "link", "set", "wan0", "up")
	run("ip", "-6", "addr", "replace", "2001:db8:1::1/64", "dev", "wan0", "nodad")
	run("ip", "-6", "route", "replace", "2001:db8:ffff::10/128", "via", "2001:db8:1::2", "dev", "wan0", "metric", "100")
	eventually("offline original NIC recovery", func() bool { got, err := probe(0, "tcp", "203.0.113.10"); return err == nil && got == "192.0.2.1" })
	verify(0)
	fmt.Println("PASS controller outage preserves service; independent local guard blocks missing NIC and restores original identity")
	run("systemctl", "stop", "ctlvps-agent.service")
	if _, err := probe(0, "tcp", "203.0.113.10"); err == nil {
		panic("agent stop left a live network lease")
	}
	run("systemctl", "start", "ctlvps-agent.service")
	// These fixture interfaces are veths with no stable device identity. The
	// offline monitoring gap cannot prove they were not replaced, even during
	// the same boot. The actual collector must retire their old IDs, so the
	// trusted plan remains blocked until an explicit controller edit rebinds.
	replacement := map[string]string{}
	eventually("restart retires unprovable virtual interface identities", func() bool {
		var registry struct{ Links []struct{ Name, ID string } }
		b, err := os.ReadFile("/var/lib/ctlvps-agent/network-interfaces.json")
		if err != nil || json.Unmarshal(b, &registry) != nil {
			return false
		}
		for _, nic := range registry.Links {
			replacement[nic.Name] = nic.ID
		}
		return replacement["wan0"] != "" && replacement["wan1"] != "" && replacement["wan0"] != interfaces["wan0"] && replacement["wan1"] != interfaces["wan1"]
	})
	for i := range nodes {
		for _, transport := range []string{"tcp", "udp"} {
			if _, err := probe(i, transport, "203.0.113.10"); err == nil {
				panic("restart reused a retired virtual interface identity")
			}
		}
	}
	offline.Store(false)
	eventually("replacement inventory reaches controller", func() bool {
		view, err := st.Network(ctx, server.ID)
		if err != nil || view.Snapshot == nil {
			return false
		}
		seen := 0
		for _, nic := range view.Snapshot.Interfaces {
			if (nic.Name == "wan0" || nic.Name == "wan1") && nic.ID == replacement[nic.Name] {
				seen++
			}
		}
		return seen == 2
	})
	interfaces = replacement
	for i, n := range nodes {
		profile, err := st.GetEgressProfile(ctx, n.Network.EgressProfileID)
		must(err)
		config, _ := json.Marshal(networkconfig.Direct{InterfaceID: interfaces[fmt.Sprint("wan", i)], Family: "dual", DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.10", Port: 15353}})
		profile, err = st.AppendEgressRevision(ctx, profile.ID, profile.CurrentRevision, profile.Name, true, config)
		must(err)
		policy := *n.Network
		policy.ListenInterfaceID, policy.EgressRevision = interfaces[fmt.Sprint("wan", i)], profile.CurrentRevision
		lastOperation = fmt.Sprintf("%032x", i+3)
		_, err = st.RequestReadyNodeNetwork(ctx, store.NodeNetworkRequest{ID: lastOperation, NodeID: n.ID, ExpectedRevision: n.NetworkRevision, Network: &policy}, domain.AuditEvent{})
		must(err)
		nodes[i], err = st.GetNode(ctx, n.ID)
		must(err)
	}
	applyQueued()
	verify(0)
	verify(1)
	fmt.Println("PASS agent stop revokes leases; restart blocks uncertain veth identities; explicit new revisions restore service")
	before := run("systemctl", "show", "ctlvps-singbox.service", "-p", "InvocationID", "--value")
	run("ip", "link", "set", "wan0", "name", "uplink0")
	eventually("rename triggers core reapply", func() bool {
		return run("systemctl", "show", "ctlvps-singbox.service", "-p", "InvocationID", "--value") != before
	})
	eventually("renamed interface traffic", func() bool { got, err := probe(0, "tcp", "203.0.113.10"); return err == nil && got == "192.0.2.1" })
	verify(0)
	verify(1)
	view, err := st.Network(ctx, server.ID)
	must(err)
	eventually("renamed interface retains identity", func() bool {
		view, err = st.Network(ctx, server.ID)
		if err != nil || view.Snapshot == nil {
			return false
		}
		for _, nic := range view.Snapshot.Interfaces {
			if nic.Name == "uplink0" {
				return nic.ID == interfaces["wan0"]
			}
		}
		return false
	})
	fmt.Println("PASS interface rename preserves ID and reconfigures the actual systemd core without a new desired hash")
	must(shares.Pause(ctx, allotments[0].ID))
	rec, err := st.LatestDesiredState(ctx, server.ID)
	must(err)
	waitApplied(rec.Revision)
	for _, transport := range []string{"tcp", "udp"} {
		if _, err := probe(0, transport, "203.0.113.10"); err == nil {
			panic("paused share still forwards")
		}
	}
	verify(1)
	must(shares.Resume(ctx, allotments[0].ID))
	rec, err = st.LatestDesiredState(ctx, server.ID)
	must(err)
	waitApplied(rec.Revision)
	verify(0)
	fmt.Println("PASS share pause/resume enforces both transports and keeps the peer usable")
	for _, sh := range allotments {
		must(shares.Delete(ctx, sh.ID))
	}
	rec, err = st.LatestDesiredState(ctx, server.ID)
	must(err)
	waitApplied(rec.Revision)
	plan, err := proxyguard.LoadNetwork(ctx)
	must(err)
	if plan == nil || len(plan.Bindings) != 0 {
		panic("final node removal left an active binding")
	}
	if run("systemctl", "show", "ctlvps-singbox.service", "-p", "MainPID", "--value") != "0" {
		panic("final node removal left core running")
	}
	fmt.Println("PASS final node deletion applies an empty versioned plan and stops the shared core")
}
