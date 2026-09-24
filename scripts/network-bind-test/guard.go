//go:build linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/nft"
	"ctlvps/internal/proxyguard"
)

func nftInput(rules string) {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if b, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("nft: %v %s", err, b))
	}
}

type runningMonitor struct {
	cancel context.CancelFunc
	done   chan error
	states chan map[int64]string
}

func startMonitor(ctx context.Context, p networkguard.Plan, collector *netinventory.Collector) *runningMonitor {
	ctx, cancel := context.WithCancel(ctx)
	m := &runningMonitor{cancel: cancel, done: make(chan error, 1), states: make(chan map[int64]string, 1)}
	monitor := networkguard.Monitor{Collect: collector.Collect,
		Renew: func(ctx context.Context, s *agentproto.NetworkSnapshot) error {
			bad, err := proxyguard.RefreshNetwork(ctx, p.Token, s)
			select {
			case <-m.states:
			default:
			}
			m.states <- bad
			return err
		},
		Revoke: func(ctx context.Context, indices map[int]bool, lost bool) error {
			return proxyguard.RevokeNetwork(ctx, p.Token, indices, lost)
		},
	}
	go func() { m.done <- monitor.Run(ctx) }()
	return m
}

func (m *runningMonitor) wait(bad bool) {
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case err := <-m.done:
			panic(fmt.Sprintf("monitor stopped: %v", err))
		case <-deadline.C:
			panic("monitor did not observe the expected binding health")
		case failures := <-m.states:
			if (failures[257] != "") == bad && failures[258] == "" {
				return
			}
		}
	}
}
func (m *runningMonitor) stop() {
	m.cancel()
	select {
	case <-m.done:
	case <-time.After(3 * time.Second):
		panic("monitor failed to stop")
	}
}

func peerWorks() {
	c, _, _, err := socksAt(1081, 1, "203.0.113.10")
	must(err)
	_, err = bufio.NewReader(c).ReadString('\n')
	c.Close()
	must(err)
}
func allFlows() []*flow {
	var out []*flow
	for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10"} {
		for _, proto := range []string{"TCP", "UDP"} {
			f := openFlow(target, proto)
			must(f.ping())
			out = append(out, f)
		}
	}
	return out
}
func closeFlows(flows []*flow) {
	for _, f := range flows {
		f.close()
	}
}
func blockedFlows(flows []*flow) {
	for _, f := range flows {
		if f.ping() == nil {
			panic("guard permitted existing " + f.protocol + " " + f.target)
		}
	}
}

func testLeaseGuard(ctx context.Context, exe string) {
	resetProbe()
	dir, err := os.MkdirTemp("/tmp", "guard-identity-")
	must(err)
	defer os.RemoveAll(dir)
	collector := netinventory.New(dir, "fixture-boot")
	defer collector.Close()
	cfg, plan := compileNodes(collector.Collect(), "udp", "dual")
	// Real accounting marks carry identity in both directions, just as in the
	// production apply path. All changes stay in this disposable net namespace.
	rules, err := nft.NodeRules([]agentproto.NodeSpec{{NodeID: 257, Core: "singbox", ListenPort: 1080}, {NodeID: 258, Core: "singbox", ListenPort: 1081}})
	must(err)
	nftInput(rules)
	must(proxyguard.Install(ctx, "add table inet ctlvps_egress\nadd chain inet ctlvps_egress output { type filter hook output priority -150; policy accept; }\n", nil))
	must(proxyguard.InstallNetwork(ctx, plan))
	monitor := startMonitor(ctx, plan, collector)
	monitor.wait(false)
	stop := launch(ctx, exe, "live-network-guard", cfg)
	flows := allFlows()
	peerWorks()
	must(proxyguard.InstallNetwork(ctx, plan))
	for _, f := range flows {
		must(f.ping())
	}
	changed := plan
	changed.Bindings = append([]networkguard.Binding(nil), plan.Bindings...)
	changed.Bindings[0].ListenPort += 50
	if err := proxyguard.InstallNetwork(ctx, changed); err == nil {
		panic("application token accepted altered guard content")
	}
	must(proxyguard.Check(ctx)) // expiring elements must not count as rule drift
	run("ip", "link", "set", "wan1", "down")
	monitor.wait(true)
	blockedFlows(flows)
	closeFlows(flows)
	peerWorks()
	fmt.Println("PASS live guard detects interface loss and blocks established TCP/UDP while preserving peer")
	restoreWan1()
	monitor.wait(false)
	flows = allFlows()
	replaceInterface() // no manually inserted fence in this experiment
	monitor.wait(true)
	blockedFlows(flows)
	closeFlows(flows)
	peerWorks()
	// Restoring the same name, addresses and ifindex cannot restore the old ID.
	time.Sleep(1100 * time.Millisecond)
	monitor.wait(true)
	fmt.Println("PASS live guard keeps same-name/same-ifindex replacement blocked until a new application")
	monitor.stop()
	stop()
	cfg, newPlan := compileNodes(collector.Collect(), "udp", "dual")
	must(proxyguard.InstallNetwork(ctx, newPlan))
	if _, err := proxyguard.RefreshNetwork(ctx, plan.Token, collector.Collect()); err == nil {
		panic("stale monitor renewed a replacement plan")
	}
	monitor = startMonitor(ctx, newPlan, collector)
	monitor.wait(false)
	stop = launch(ctx, exe, "reapplied-network-guard", cfg)
	flows = allFlows()
	closeFlows(flows)
	peerWorks()
	monitor.stop()
	// Watchdog repair restores static rules, with no stale readiness grants.
	run("nft", "flush", "chain", "inet", networkguard.Table, "output")
	must(proxyguard.Check(ctx))
	if _, err := tcp("203.0.113.10"); err == nil {
		panic("watchdog repair revived stale health")
	}
	fmt.Println("PASS application token rejects stale renewal; watchdog repair starts closed")
	// Protect just one consumer to prove timeout expiry does not stop an
	// unrelated node in the same proxy process. No monitor runs during expiry.
	_, last := compileNodes(collector.Collect(), "udp", "dual")
	last.Bindings = last.Bindings[:1]
	must(proxyguard.InstallNetwork(ctx, last))
	bad, err := proxyguard.RefreshNetwork(ctx, last.Token, collector.Collect())
	must(err)
	if len(bad) != 0 {
		panic(fmt.Sprint(bad))
	}
	flows = allFlows()
	time.Sleep(networkguard.Lease + 500*time.Millisecond)
	blockedFlows(flows)
	closeFlows(flows)
	peerWorks()
	stop()
	fmt.Println("PASS kernel lease expiry blocks old flows without an agent, monitor or systemd stop; unrelated node survives")
}
