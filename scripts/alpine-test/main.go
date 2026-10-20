//go:build linux

// Exercises production core/OpenRC/nft boundaries in a disposable container.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ctlvps/internal/agent"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/nft"
	"ctlvps/internal/openrc"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func run(args ...string) string {
	b, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("%s: %v: %s", args[0], err, b))
	}
	return strings.TrimSpace(string(b))
}
func write(path string, b []byte, mode os.FileMode) {
	must(os.MkdirAll(filepath.Dir(path), 0755))
	must(os.WriteFile(path, b, mode))
}
func writeJSON(path string, value any) { b, e := json.Marshal(value); must(e); write(path, b, 0600) }
func eventually(label string, f func() bool) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	panic("timed out: " + label)
}
func process(args ...string) func() {
	c := exec.Command(args[0], args[1:]...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	must(c.Start())
	return func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL); _ = c.Wait() }
}

func main() {
	debug.SetMemoryLimit(48 << 20)
	if os.Getenv("CTLVPS_ALPINE_FIXTURE") != "1" {
		panic("explicit isolated fixture required")
	}
	if _, e := os.Stat("/.dockerenv"); e != nil {
		panic("disposable Docker container required")
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mita-payload":
			mitaPayload()
			return
		case "echo":
			echo()
			return
		case "probe":
			must(probe())
			return
		case "probe-target":
			must(probeTarget(os.Args[2], os.Args[3]))
			return
		case "mark-probe":
			must(markProbe(os.Args[2], os.Args[3], os.Args[4]))
			return
		case "echo-local":
			echoAt([]string{"127.0.0.1:18080"})
			return
		case "agent-lifecycle":
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			must(openrc.CheckEnvironment(ctx))
			verifyAgentLifecycle(ctx, core.NewSystemd())
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	must(openrc.CheckEnvironment(ctx))
	run("ip", "link", "set", "lo", "up")
	run("ip", "netns", "add", "alpine-peer")
	defer run("ip", "netns", "del", "alpine-peer")
	run("ip", "link", "add", "alpine-wan", "type", "veth", "peer", "name", "alpine-peer0")
	run("ip", "link", "set", "alpine-peer0", "netns", "alpine-peer")
	run("ip", "addr", "add", "198.51.100.1/24", "dev", "alpine-wan")
	run("ip", "link", "set", "alpine-wan", "up")
	run("ip", "netns", "exec", "alpine-peer", "ip", "addr", "add", "198.51.100.2/24", "dev", "alpine-peer0")
	run("ip", "netns", "exec", "alpine-peer", "ip", "link", "set", "alpine-peer0", "up")
	run("ip", "netns", "exec", "alpine-peer", "ip", "link", "set", "lo", "up")
	run("ip", "netns", "exec", "alpine-peer", "ip", "addr", "add", "203.0.113.10/32", "dev", "lo")
	run("ip", "netns", "exec", "alpine-peer", "ip", "addr", "add", "10.123.0.2/24", "dev", "alpine-peer0")
	run("ip", "addr", "add", "10.123.0.1/24", "dev", "alpine-wan")
	run("ip", "route", "add", "203.0.113.10/32", "via", "198.51.100.2", "dev", "alpine-wan")
	stopEcho := process("ip", "netns", "exec", "alpine-peer", "/fixtures/integration", "echo")
	defer stopEcho()
	stopLocal := process("/fixtures/integration", "echo-local")
	defer stopLocal()
	paths := core.DefaultPaths("/var/lib/ctlvps-agent")
	write("/etc/ctlvps/fixture-only", []byte("fixture management secret"), 0600)
	if len(os.Args) > 1 && os.Args[1] == "mita" {
		testMita(ctx, paths)
		return
	}
	must(os.MkdirAll(paths.BinDir, 0755))
	source, e := os.Open("/fixtures/sing-box")
	must(e)
	binary, e := os.OpenFile(paths.BinDir+"/sing-box", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	must(e)
	sum := sha256.New()
	_, e = io.Copy(io.MultiWriter(binary, sum), source)
	must(e)
	must(source.Close())
	must(binary.Close())
	writeJSON(paths.BinDir+"/sing-box.trusted", map[string]string{"Version": "1.14.1", "SHA256": hex.EncodeToString(sum.Sum(nil))})
	// A local fixture credential, never a controller or production node token.
	ds := &agentproto.DesiredState{ServerID: 1, Revision: 1, Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.14.1"}}, Tuning: agentproto.Tuning{MemoryMaxMB: 96, GoMemLimitMB: 64, LimitNOFILE: 8192, RestartSec: 1}, Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 30001, Params: map[string]any{"method": "aes-128-gcm", "password": "fixture-only"}}}}
	ds.Hash = agentproto.ContentHash(ds)
	sd := core.NewSystemd()
	m := nft.New()
	sb := core.NewSingBox(paths, sd)
	must(sd.EnsureProxyBudget(ctx, ds.Tuning))
	group, e := sd.EnsureSingBoxSlice(ctx, false)
	must(e)
	must(m.EnsureNodes(ctx, ds.Nodes))
	must(m.EnsureResourceEgress(ctx, ds.Nodes, map[int64]string{1: group}, nil, ""))
	// Simulate a retained root-owned log with unusable mode from an old install.
	// Applying the dedicated proxy identity must repair the file, not give the
	// proxy ownership of its root-managed parent directory.
	must(os.MkdirAll(paths.LogDir, 0755))
	must(os.WriteFile(paths.LogPath(), nil, 0000))
	must(os.Chmod(paths.LogPath(), 0000))
	_, e = sb.ApplyResources(ctx, ds, ds.Nodes, nil)
	must(e)
	must(sb.FinishActivation(ctx, true))
	verifyRPCAndGuard(ctx)
	verifyHelperRefresh(ctx)
	defer sb.Stop(context.Background())
	defer sd.StopDisable(context.Background(), "ctlvps-proxy-guard.service")
	cfg := natconfig.Config{Schema: 1, Interface: "alpine-wan", Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001}}}
	must(m.EnsureNAT(ctx, cfg, ds.Nodes, nil))
	pid, e := openrc.MainPID("ctlvps-singbox.service")
	must(e)
	if pid == 0 {
		panic("missing official proxy")
	}
	verifyIsolation(pid)
	// Probe the production permission cgroup directly, independent of the
	// core's own ip_is_private reject route and its listener credentials.
	servicePath := group + "/ctlvps-singbox.service"
	for _, mark := range []string{"0", "0x43000001", "0x43000002"} {
		for _, host := range []string{"127.0.0.1", "10.123.0.2", "203.0.113.10"} {
			err := exec.Command("/fixtures/integration", "mark-probe", host, mark, servicePath).Run()
			allowed := host == "203.0.113.10" && mark == "0x43000001"
			if (err == nil) != allowed {
				panic(fmt.Sprintf("production cgroup boundary host=%s mark=%s allowed=%v got=%v", host, mark, allowed, err))
			}
		}
	}
	// The client runs outside the server's cgroup and in a separate network ns.
	writeJSON("/tmp/alpine-client.json", map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": 1080}}, "outbounds": []any{map[string]any{"type": "shadowsocks", "server": "198.51.100.1", "server_port": 10001, "method": "aes-128-gcm", "password": "fixture-only"}}})
	stopClient := process("ip", "netns", "exec", "alpine-peer", "/fixtures/sing-box", "run", "-c", "/tmp/alpine-client.json")
	defer stopClient()
	probeCmd := func() error {
		return exec.Command("ip", "netns", "exec", "alpine-peer", "/fixtures/integration", "probe").Run()
	}
	eventually("SS TCP/UDP through mapped port", func() bool { return probeCmd() == nil })
	for _, host := range []string{"127.0.0.1", "10.123.0.2"} {
		for _, network := range []string{"tcp", "udp"} {
			if exec.Command("ip", "netns", "exec", "alpine-peer", "/fixtures/integration", "probe-target", network, host).Run() == nil {
				panic("private proxy target allowed: " + network + " " + host)
			}
		}
	}
	counters, e := m.ReadNodes(ctx)
	must(e)
	if len(counters) != 1 || counters[0].Rx <= 0 || counters[0].Tx <= 0 {
		panic("real node traffic not counted in both directions")
	}
	before := counters[0]
	must(sd.StopUnits(ctx, []string{"ctlvps-singbox.service"}))
	must(sd.StartUnits(ctx, []string{"ctlvps-singbox.service"}))
	eventually("proxy restart", func() bool { return probeCmd() == nil })
	counters, e = m.ReadNodes(ctx)
	must(e)
	if counters[0].Rx <= before.Rx || counters[0].Tx <= before.Tx {
		panic("restart erased or stalled accounting")
	}
	// The watchdog is independent of agent/controller polling and repairs its
	// published fixed firewall after deletion, preserving private egress denial.
	run("nft", "delete", "table", "inet", "ctlvps_egress")
	eventually("independent OpenRC guard repair", func() bool { return exec.Command("nft", "list", "table", "inet", "ctlvps_egress").Run() == nil })
	eventually("guard restores proven service", func() bool { return probeCmd() == nil })
	ds.Nodes[0].Blocked = true
	must(m.EnsureNodes(ctx, ds.Nodes))
	if probeCmd() == nil {
		panic("blocked NAT node continued serving TCP/UDP")
	}
	ds.Nodes[0].Blocked = false
	must(m.EnsureNodes(ctx, ds.Nodes))
	eventually("unblocked NAT node", func() bool { return probeCmd() == nil })
	// Add a real unrelated child to this cgroup, then stop the managed service.
	child := exec.Command("sleep", "120")
	must(child.Start())
	defer child.Wait()
	serviceGroup := "/sys/fs/cgroup" + group + "/ctlvps-singbox.service"
	must(os.WriteFile(serviceGroup+"/cgroup.procs", []byte(strconv.Itoa(child.Process.Pid)), 0600))
	must(sd.StopUnits(ctx, []string{"ctlvps-singbox.service"}))
	eventually("whole-cgroup stop", func() bool {
		b, e := os.ReadFile(serviceGroup + "/cgroup.events")
		return e == nil && strings.Contains(string(b), "populated 0")
	})
	if _, e := os.Stat(fmt.Sprintf("/proc/%d", pid)); e == nil {
		panic("old proxy survived whole-cgroup stop")
	}
	must(sd.StopDisable(ctx, "ctlvps-proxy-guard.service"))
	verifyAgentLifecycle(ctx, sd)
	fmt.Println("PASS Alpine OpenRC official-musl SS TCP/UDP, NAT, counters/restart/block, independent firewall guard, sandbox and whole-cgroup stop")
}

func verifyAgentLifecycle(ctx context.Context, sd *core.Systemd) {
	state := &agent.State{ServerURL: "https://controller.invalid", AgentToken: "fixture-only", ServerID: 1, PollIntervalSec: 10, MeteringV1: true}
	must(state.Save("/var/lib/ctlvps-agent"))
	script, e := openrc.Script("ctlvps-agent.service")
	must(e)
	write("/etc/init.d/ctlvps-agent", []byte(script), 0755)
	must(sd.EnableRestart(ctx, "ctlvps-agent.service"))
	pid := 0
	eventually("actual OpenRC agent daemon", func() bool { pid, _ = openrc.MainPID("ctlvps-agent.service"); return pid > 0 })
	group := "/sys/fs/cgroup/ctlvps.slice/ctlvps-agent.service"
	b, e := os.ReadFile(group + "/memory.max")
	must(e)
	if strings.TrimSpace(string(b)) != "134217728" {
		panic("agent lacks 128MiB hard budget")
	}
	b, e = os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	must(e)
	if !bytes.Contains(b, []byte("GOMEMLIMIT=64MiB\x00")) {
		panic("agent lacks 64MiB Go soft limit")
	}
	eventually("sticky OpenRC compatibility", func() bool { s, e := agent.LoadState("/var/lib/ctlvps-agent"); return e == nil && s.OpenRCVersion == 1 })
	eventually("completed agent startup receipt", func() bool { return openrc.AgentReady() == nil })
	const readyFile = "/run/ctlvps-openrc/agent-ready.json"
	readyBody, e := os.ReadFile(readyFile)
	must(e)
	var ready struct {
		PID   int    `json:"pid"`
		Start string `json:"start"`
	}
	must(json.Unmarshal(readyBody, &ready))
	defer func() { must(os.WriteFile(readyFile, readyBody, 0600)) }()
	wrongPID := ready
	wrongPID.PID++
	writeJSON(readyFile, wrongPID)
	if openrc.AgentReady() == nil {
		panic("agent readiness accepted a receipt from another PID")
	}
	wrongStart := ready
	wrongStart.Start = "0"
	writeJSON(readyFile, wrongStart)
	if openrc.AgentReady() == nil {
		panic("agent readiness accepted a receipt from an earlier process lifetime")
	}
	must(os.WriteFile(readyFile, readyBody, 0600))
	must(openrc.AgentReady())
	// Best-effort worker observation; no fabricated worker is needed when the
	// controller is offline. Every observed worker must inherit agent bounds.
	for _, item := range []string{"memory.current", "memory.peak", "memory.events"} {
		b, e := os.ReadFile("/sys/fs/cgroup/" + item)
		if e == nil {
			fmt.Printf("fixture container %s: %s\n", item, strings.TrimSpace(string(b)))
		}
	}
	must(sd.StopDisable(ctx, "ctlvps-agent.service"))
	eventually("whole agent cgroup stop", func() bool {
		b, e := os.ReadFile(group + "/cgroup.events")
		return e == nil && strings.Contains(string(b), "populated 0")
	})
	if openrc.AgentReady() == nil {
		panic("stopped agent retained successful readiness")
	}
	fmt.Println("PASS actual OpenRC agent start/stop, startup receipt PID/lifetime checks, 128MiB/64MiB budgets and persisted OpenRC downgrade protection")
}

func verifyIsolation(pid int) {
	verifyProxyIsolation(pid, 0x2400, "ctlvps-singbox.service", "/usr/local/bin/ctlvps-agent proxy-exec singbox run public")
}
func verifyProxyIsolation(pid int, caps uint64, unit, command string) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	must(e)
	fields := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			fields[k] = strings.TrimSpace(v)
		}
	}
	if strings.Fields(fields["Uid"])[0] == "0" || fields["NoNewPrivs"] != "1" || fields["Seccomp"] != "2" {
		panic("proxy UID or kernel seccomp isolation missing")
	}
	for _, key := range []string{"CapEff", "CapPrm", "CapBnd", "CapAmb"} {
		n, e := strconv.ParseUint(fields[key], 16, 64)
		must(e)
		if n != caps {
			panic("proxy capabilities differ: " + key)
		}
	}
	must(openrc.VerifyProxy(unit, command))
	for _, ns := range []string{"mnt", "ipc", "uts"} {
		ours, e := os.Readlink("/proc/self/ns/" + ns)
		must(e)
		proxy, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/%s", pid, ns))
		must(e)
		if ours == proxy {
			panic("proxy namespace shared: " + ns)
		}
	}
	root := fmt.Sprintf("/proc/%d/root", pid)
	if _, e := os.Stat(root + "/etc/ctlvps/fixture-only"); e == nil {
		panic("proxy can access management configuration")
	}
	if b, e := os.ReadFile("/sys/fs/cgroup/ctlvps.slice/ctlvps-proxy.slice/memory.max"); e != nil || !bytes.Equal(bytes.TrimSpace(b), []byte("100663296")) {
		panic("aggregate proxy cgroup budget missing")
	}
}
