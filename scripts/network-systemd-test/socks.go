//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/proxyguard"
	"ctlvps/internal/secureupdate"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
)

// Real admin template/binding API, controller desired/heartbeat and
// agent/systemd paths. Shares and network fixtures are prepared locally.
func testSOCKSSystemd(ctx context.Context, st *store.Store, d *desired.Builder, shares *share.Manager, server domain.Server, interfaces map[string]string, offline *atomic.Bool, failedHeartbeat <-chan struct{}, admin *fixtureAdmin) {
	if os.Getenv("NETWORK_TEST_CLIENT") != "shadowsocks-rust" {
		panic("SOCKS systemd fixture requires independent rust SS client")
	}
	private := os.Getenv("NETWORK_SYSTEMD_CASE") == "socks-private"
	primary, alternate := "192.0.2.2", "192.0.2.3"
	if private {
		primary, alternate = "10.23.0.2", "10.23.0.3"
	}
	run("ip", "netns", "exec", "landing", "ip", "addr", "add", "192.0.2.3/24", "dev", "peer0")
	ins, outs, routes := []any{}, []any{}, []any{}
	for i, ip := range []string{primary, alternate, "198.51.100.2"} {
		tag := fmt.Sprint("upstream", i)
		port, source6 := 11080, "2001:db8:1::2"
		if i == 2 {
			port = 11081
			source6 = "2001:db8:2::2"
		}
		ins = append(ins, map[string]any{"type": "socks", "tag": tag, "listen": ip, "listen_port": port, "users": []any{map[string]any{"username": "fixture", "password": "fixture-only"}}})
		outs = append(outs, map[string]any{"type": "direct", "tag": tag, "inet4_bind_address": ip, "inet6_bind_address": source6})
		routes = append(routes, map[string]any{"inbound": []string{tag}, "action": "route", "outbound": tag})
	}
	writeJSON("/tmp/fixture-upstreams.json", map[string]any{"log": map[string]any{"level": "error"}, "inbounds": ins, "outbounds": outs, "route": map[string]any{"rules": routes}})
	stopUpstream := process(ctx, "ip", "netns", "exec", "landing", "/opt/ctlvps/bin/sing-box", "run", "-c", "/tmp/fixture-upstreams.json")
	defer stopUpstream()
	for _, address := range []string{primary + ":11080", alternate + ":11080", "198.51.100.2:11081"} {
		eventually("SOCKS upstream listener", func() bool {
			c, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err != nil {
				return false
			}
			c.Close()
			return true
		})
	}
	nodes := []domain.Node{}
	var rootGrants []networkconfig.TransportGrant
	setGrants := func(grants []networkconfig.TransportGrant) {
		// Atomic root-owned policy replacement, only in the disposable fixture.
		writeJSON("/etc/ctlvps/.fixture-security.json", secureupdate.Policy{Schema: 1, Actions: []string{"agent.configure"}, TransportGrants: grants})
		must(os.Rename("/etc/ctlvps/.fixture-security.json", "/etc/ctlvps/security.json"))
	}
	allotments := []domain.Share{}
	waitApplied := func(rev int64) {
		eventually("SOCKS real agent applied", func() bool {
			ag, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && ag.AppliedRevision == rev && ag.ApplyError == ""
		})
	}
	for i, host := range []string{"192.0.2.1", "198.51.100.1"} {
		sh := domain.Share{Name: fmt.Sprint("SOCKS fixture ", i), Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
		_, err := shares.Create(ctx, &sh)
		must(err)
		allotments = append(allotments, sh)
		list, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
		must(err)
		if len(list) != 1 {
			panic("missing SOCKS share node")
		}
		outer := networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: []string{primary, "198.51.100.2"}[i], Port: 15353}}
		if i == 0 {
			outer.InterfaceID = interfaces["wan0"]
			outer.SourceIPv4 = &networkconfig.Address{InterfaceID: outer.InterfaceID, Address: host}
		}
		cfg := networkconfig.SOCKS5{Server: fmt.Sprintf("truncate.refresh%d.fixture.test", i), ServerPort: 11080 + i, Authentication: "password", UDP: true, Family: "dual", ConnectTimeoutSeconds: 2,
			DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.10", Port: 15353}, Outer: outer}
		profilePath := fmt.Sprintf("/api/v1/servers/%d/egress-profiles", server.ID)
		body := map[string]any{"expected_revision": 0, "name": fmt.Sprint("SOCKS ", i), "kind": "socks5", "enabled": true, "config": cfg, "credentials": networkconfig.SOCKS5Credentials{Username: "fixture", Password: "fixture-only"}, "action": "create"}
		var review store.EgressPreview
		admin.request("POST", profilePath+"/preview", body, 200, &review)
		if !review.Ready {
			panic("SOCKS profile review not ready")
		}
		delete(body, "action")
		body["operation_id"], body["expected_impact"] = fmt.Sprintf("%032x", 100+i), review.Impact.Token
		var op store.NetworkOperation
		admin.request("POST", profilePath, body, 202, &op)
		policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "override", OnUnavailable: "block", EgressProfileID: op.ResourceID, EgressRevision: 1}
		if i == 0 {
			policy.ListenMode = "address"
			policy.ListenAddress = host
			policy.ListenInterfaceID = interfaces["wan0"]
		}
		bindingPath := fmt.Sprintf("/api/v1/nodes/%d/network", list[0].ID)
		binding := map[string]any{"network": policy, "advertise_host": host}
		var ready store.NetworkReadiness
		admin.request("POST", bindingPath+"/preview", binding, 200, &ready)
		if private && i == 0 {
			if ready.Ready {
				panic("private bootstrap admitted without local authority")
			}
			rootGrants = []networkconfig.TransportGrant{
				{NodeID: list[0].ID, EgressProfileID: op.ResourceID, Purpose: "socks5", Network: "tcp", Address: "10.23.0.0/24", Port: 11080},
				{NodeID: list[0].ID, EgressProfileID: op.ResourceID, Purpose: "socks5", Network: "udp", Address: "10.23.0.0/24", Port: 40000, PortEnd: 60000},
				{NodeID: list[0].ID, EgressProfileID: op.ResourceID, Purpose: "bootstrap_dns", Network: "tcp", Address: primary, Port: 15353},
				{NodeID: list[0].ID, EgressProfileID: op.ResourceID, Purpose: "bootstrap_dns", Network: "udp", Address: primary, Port: 15353},
			}
			setGrants(rootGrants)
			eventually("root transport authorization reaches diagnostics", func() bool {
				ag, err := st.GetAgentByServer(ctx, server.ID)
				var diag agentproto.Diagnostics
				return err == nil && json.Unmarshal(ag.Diagnostics, &diag) == nil && diag.NetworkTransportVersion == 1 && len(diag.TransportGrants) == len(rootGrants)
			})
			admin.request("POST", bindingPath+"/preview", binding, 200, &ready)
		}
		if !ready.Ready {
			panic(fmt.Sprintf("SOCKS binding review not ready: %+v", ready.Checks))
		}
		binding["operation_id"], binding["expected_revision"], binding["expected_impact"] = fmt.Sprintf("%032x", 200+i), list[0].NetworkRevision, ready.Impact.Token
		admin.request("PUT", bindingPath, binding, 202, &op)
		// Complete one reviewed edit before creating the next share, which
		// otherwise correctly supersedes this server's pending generation.
		must(d.ReconcileNetworkOperations(ctx))
		accepted, err := st.LatestDesiredState(ctx, server.ID)
		must(err)
		waitApplied(accepted.Revision)
		var receipt store.NetworkOperation
		admin.request("GET", "/api/v1/network/operations/"+op.ID, nil, 200, &receipt)
		if receipt.Status != "applied" {
			panic("management binding lacked exact real-agent application receipt")
		}
		n, err := st.GetNode(ctx, list[0].ID)
		must(err)
		nodes = append(nodes, n)
	}
	must(d.ReconcileNetworkOperations(ctx))
	rec, err := st.LatestDesiredState(ctx, server.ID)
	must(err)
	waitApplied(rec.Revision)
	fmt.Println("PASS admin authentication -> SOCKS preview/create -> node preview/bind -> exact real-agent receipt")
	verifySOCKS := func(i int, source string) {
		for _, target := range []string{"203.0.113.10", "2001:db8:ffff::10", "binding.test", "truncate.binding.test"} {
			want := source
			if strings.Contains(target, ":") {
				want = fmt.Sprintf("2001:db8:%d::2", i+1)
			}
			for _, transport := range []string{"tcp", "udp"} {
				got, e := probe(i, transport, target)
				if e != nil || got != want {
					dumpNetworkFailure()
					panic(fmt.Sprintf("SOCKS node %d %s target %s: %q want %q: %v", i, transport, target, got, want, e))
				}
			}
		}
	}
	for i, n := range nodes {
		stop := startClient(ctx, i, n)
		defer stop()
		eventually("SOCKS independent client ready", func() bool { _, e := probe(i, "ready", ""); return e == nil })
	}
	verifySOCKS(0, primary)
	verifySOCKS(1, "198.51.100.2")
	fmt.Println("PASS real agent/helper/systemd/general firewall: authenticated domain SOCKS5, TCP/UDP IPv4/IPv6 and both DNS paths")
	load := func() *networkguard.Plan {
		p, e := proxyguard.LoadNetwork(ctx)
		must(e)
		if p == nil {
			panic("missing SOCKS plan")
		}
		return p
	}
	find := func(p *networkguard.Plan, id int64) networkguard.Binding {
		for _, b := range p.Bindings {
			if b.NodeID == id {
				return b
			}
		}
		panic("missing SOCKS binding")
	}
	initial := load()
	firstPin := find(initial, nodes[0].ID).Applied.SOCKS5
	invocation := run("systemctl", "show", "ctlvps-singbox.service", "-p", "InvocationID", "--value")
	eventually("same-address DNS renewal", func() bool {
		p := load()
		b := find(p, nodes[0].ID)
		return b.Applied.SOCKS5 != nil && b.Applied.SOCKS5.ResolvedAt.After(firstPin.ResolvedAt)
	})
	if load().Token != initial.Token || run("systemctl", "show", "ctlvps-singbox.service", "-p", "InvocationID", "--value") != invocation {
		panic("same-address DNS renewal restarted core or replaced network rules")
	}
	verifySOCKS(0, primary)
	fmt.Println("PASS same-IP DNS refresh preserves core invocation and active network plan")
	eventually("SOCKS counters reach shares", func() bool {
		for _, sh := range allotments {
			v, e := st.GetShare(ctx, sh.ID)
			if e != nil || v.UsedUpload == 0 || v.UsedDownload == 0 {
				return false
			}
		}
		return true
	})
	offline.Store(true)
	select {
	case <-failedHeartbeat:
	case <-time.After(45 * time.Second):
		panic("controller outage not observed")
	}
	if private {
		testPrivateSOCKSRuntime(ctx, nodes, rootGrants, setGrants, load, find, verifySOCKS, primary)
	}
	must(os.WriteFile("/tmp/ctlvps-fixture-dns-rebind", []byte("alternate"), 0644))
	eventually("offline DNS switches endpoint", func() bool {
		b := find(load(), nodes[0].ID)
		return !b.Pending && b.Applied.SOCKS5 != nil && b.Applied.SOCKS5.Address == alternate
	})
	verifySOCKS(0, alternate)
	verifySOCKS(1, "198.51.100.2")
	fmt.Println("PASS controller offline: local DNS refresh switches actual TCP/UDP traffic to the new guarded endpoint")
	must(os.WriteFile("/tmp/ctlvps-fixture-dns-rebind", []byte("private"), 0644))
	eventually("unsafe DNS answer fences node", func() bool { return find(load(), nodes[0].ID).Pending })
	if _, e := probe(0, "tcp", "203.0.113.10"); e == nil {
		panic("unsafe DNS answer left old business usable")
	}
	verifySOCKS(1, "198.51.100.2")
	must(os.WriteFile("/tmp/ctlvps-fixture-dns-rebind", []byte("alternate"), 0644))
	eventually("DNS recovery without controller", func() bool {
		b := find(load(), nodes[0].ID)
		return !b.Pending && b.Applied.SOCKS5 != nil && b.Applied.SOCKS5.Address == alternate
	})
	verifySOCKS(0, alternate)
	fmt.Println("PASS unsafe DNS answer blocks only its node; valid answer recovers without controller access")
	beforeRestart := find(load(), nodes[1].ID).Applied.SOCKS5.ResolvedAt
	run("systemctl", "restart", "ctlvps-agent.service")
	eventually("offline restart recovers cached domain intent", func() bool {
		b := find(load(), nodes[1].ID)
		return !b.Pending && b.Applied.SOCKS5 != nil && b.Applied.SOCKS5.ResolvedAt.After(beforeRestart)
	})
	verifySOCKS(1, "198.51.100.2")
	// The veth-bound first node cannot prove identity continuity across restart.
	if !find(load(), nodes[0].ID).Pending {
		panic("offline cache silently rebound an uncertain veth identity")
	}
	fmt.Println("PASS offline agent restart uses exact cached intent; unbound peer recovers while retired veth binding remains blocked")
	for _, sh := range allotments {
		must(shares.Delete(ctx, sh.ID))
	}
	rec, err = st.LatestDesiredState(ctx, server.ID)
	must(err)
	offline.Store(false)
	run("systemctl", "restart", "ctlvps-agent.service")
	waitApplied(rec.Revision)
	if len(load().Bindings) != 0 {
		panic("transit cleanup left bindings")
	}
	fmt.Println("PASS capable agent applies final empty transit cleanup after reconnect")
}
