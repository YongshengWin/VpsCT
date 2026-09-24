//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

// A focused compiler/kernel check, not an agent/API or watchdog acceptance run.
// The earlier UDP qualification remains separate and retains its failing check.
func testCompiledForwardTCP(ctx context.Context, exe string) {
	dir, err := os.MkdirTemp("/tmp", "forward-compiled-")
	must(err)
	defer os.RemoveAll(dir)
	collector := netinventory.New(dir, "fixture-boot")
	defer collector.Close()
	snapshot := collector.Collect()
	ids := map[string]string{}
	for _, iface := range snapshot.Interfaces {
		ids[iface.Name] = iface.ID
	}
	var forwards []agentproto.ForwardSpec
	var bindings []networkguard.Binding
	for i := 0; i < 2; i++ {
		listen, target, family, source, cidr := "192.0.2.1", "203.0.113.10", "ipv4", "198.51.100.1", "192.0.2.2/32"
		if i == 1 {
			listen, target, family, source, cidr = "2001:db8:1::1", "2001:db8:ffff::10", "ipv6", "2001:db8:2::1", "2001:db8:1::2/128"
		}
		f := agentproto.ForwardSpec{ForwardID: int64(i + 1), Revision: 1, Config: networkconfig.Forward{
			ListenMode: "address", ListenAddress: listen, ListenInterfaceID: ids["wan0"], ListenPort: 21011 + i,
			Network: "tcp", TargetHost: target, TargetPort: 18081, SourceMode: "cidr", SourceCIDRs: []string{cidr},
			MaxTCPConnections: 2, EgressProfileID: 1, EgressRevision: 1,
		}, Direct: &networkconfig.Direct{InterfaceID: ids["wan1"], Family: family, DNS: networkconfig.Resolver{Transport: "udp", Address: target, Port: 53}}}
		if i == 0 {
			f.Direct.SourceIPv4 = &networkconfig.Address{InterfaceID: ids["wan1"], Address: source}
		} else {
			f.Direct.SourceIPv6 = &networkconfig.Address{InterfaceID: ids["wan1"], Address: source}
		}
		resolved, err := netinventory.ResolveForward(f, snapshot, false, time.Now())
		must(err)
		f.RuntimeNetwork = &resolved
		forwards = append(forwards, f)
		cfg := f.Config
		bindings = append(bindings, networkguard.Binding{ForwardID: f.ForwardID, Forward: &cfg, Core: "singbox", ListenPort: cfg.ListenPort, Wanted: cfg.BindingPolicy(), Applied: resolved})
	}
	plan, err := networkguard.New(bindings)
	must(err)
	rules, err := plan.Rules()
	must(err)
	admission, err := plan.AdmissionRules(networkguard.Plan{Token: strings.Repeat("0", 32)}, true)
	must(err)
	nftInput(rules + admission)
	ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.12.14"}}}
	cfg, err := (&core.SingBox{}).BuildResourceConfig(ds, nil, forwards)
	must(err)
	cfg["log"] = map[string]any{"level": "error"}
	origin := exec.CommandContext(ctx, "ip", "netns", "exec", "landing", exe, "forward-origin")
	origin.Stderr = os.Stderr
	must(origin.Start())
	defer func() { _ = origin.Process.Kill(); _ = origin.Wait() }()
	stop := launchProduction(ctx, exe, "forward-compiled", "", cfg)
	defer stop()
	time.Sleep(250 * time.Millisecond)
	renew := func(p networkguard.Plan) {
		ready, bad := p.HealthyResources(collector.Collect(), time.Now())
		if len(bad) != 0 {
			panic(fmt.Sprint(bad))
		}
		rules, err := p.RenewResources(ready)
		must(err)
		nftInput(rules)
	}
	blocked := newForwardClient(ctx, exe, "tcp", "192.0.2.1:21011", "")
	forwardBlocked(blocked.ready)
	blocked.close()
	renew(plan)
	a := newForwardClient(ctx, exe, "tcp", "192.0.2.1:21011", "")
	defer a.close()
	b := newForwardClient(ctx, exe, "tcp", "192.0.2.1:21011", "")
	defer b.close()
	v6 := newForwardClient(ctx, exe, "tcp", "[2001:db8:1::1]:21012", "")
	defer v6.close()
	forwardOK(a.ready, "198.51.100.1:")
	forwardOK(b.ready, "198.51.100.1:")
	forwardOK(v6.ready, "[2001:db8:2::1]:")
	fmt.Println("PASS compiled forward TCP IPv4/IPv6 fixed target and wan1 source; empty leases deny")
	// Replace the guard and dispatch, while preserving connlimit expressions.
	next, err := networkguard.New(bindings)
	must(err)
	rules, err = next.Rules()
	must(err)
	admission, err = next.AdmissionRules(plan, false)
	must(err)
	nftInput(rules + admission)
	renew(next)
	third := newForwardClient(ctx, exe, "tcp", "192.0.2.1:21011", "")
	forwardBlocked(third.ready)
	third.close()
	forwardOK(a.request("probe"), "198.51.100.1:")
	forwardOK(b.request("probe"), "198.51.100.1:")
	fmt.Println("PASS compiled forward connection budget survives guard and dispatch replacement")
	acl := *bindings[0].Forward
	acl.SourceCIDRs = nil
	bindings[0].Forward = &acl
	next, err = networkguard.New(bindings)
	must(err)
	rules, err = next.Rules()
	must(err)
	nftInput(rules)
	renew(next)
	forwardBlocked(a.request("probe"))
	forwardOK(v6.request("probe"), "[2001:db8:2::1]:")
	fmt.Println("PASS compiled forward empty source ACL revokes an existing TCP connection; peer remains live")
}
