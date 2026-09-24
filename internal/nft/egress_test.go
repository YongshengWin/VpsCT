package nft

import (
	"ctlvps/internal/agentproto"
	"strings"
	"testing"
)

func TestEgressIsBoundToNodeSockets(t *testing.T) {
	nodes := []agentproto.NodeSpec{{NodeID: 1, Core: "singbox"}, {NodeID: 2, Core: "snell"}, {NodeID: 3, Core: "singbox", AllowPrivate: true}}
	s, e := EgressRules(nodes, map[int64]string{1: "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice", 2: "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-n2.slice", 3: "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-private.slice"}, []string{"127.0.0.53"})
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"ctlvps-proxy-public.slice", "socket cgroupv2 level 3", "fib daddr type local drop", "169.254.0.0/16", "th dport 53 accept"} {
		if !strings.Contains(s, want) {
			t.Fatal(want)
		}
	}
	for _, line := range strings.Split(s, "\n") {
		// Root-created bootstrap sockets have a separate mark namespace. Its
		// dispatcher must enter a UID-guarded chain, never accept by mark alone.
		if line == "add rule inet ctlvps_egress output meta mark & 0xff000000 { 0x44000000, 0x46000000 } jump ctlvps_bootstrap" {
			continue
		}
		if strings.Contains(line, "meta mark") && (!strings.Contains(line, "socket cgroupv2") || !strings.HasSuffix(line, " drop")) {
			t.Fatal("mark became authorization", line)
		}
	}
	const bootstrapGuard = "add rule inet ctlvps_egress ctlvps_bootstrap meta skuid != 0 drop\nadd rule inet ctlvps_egress ctlvps_bootstrap fib daddr type local drop\nadd rule inet ctlvps_egress ctlvps_bootstrap drop\n"
	if !strings.Contains(s, bootstrapGuard) {
		t.Fatal("nodes without bootstrap DNS must not authorize any bootstrap socket")
	}
	if _, e = EgressRules(nodes, nil, nil); e == nil {
		t.Fatal("missing cgroup accepted")
	}
}

func TestMarkAllowlistPreservesACMEAndRejectsOtherGroups(t *testing.T) {
	nodes := []agentproto.NodeSpec{{NodeID: 1, Core: "singbox"}, {NodeID: 2, Core: "singbox", Blocked: true}, {NodeID: 3, Core: "singbox", AllowPrivate: true}}
	groups := map[int64]string{1: "/ctlvps.slice/public", 2: "/ctlvps.slice/public", 3: "/ctlvps.slice/private"}
	s, e := EgressRules(nodes, groups, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(s, "priority -150") || !strings.Contains(s, "meta mark != { 0x43000001 } drop") || !strings.Contains(s, "meta mark != { 0x43000003 } drop") {
		t.Fatal(s)
	}
	if strings.Contains(s, "0x43000002") || strings.Contains(s, "0x00000000") {
		t.Fatal("blocked/unmarked identity accepted")
	}
	nodes[0].Cert = &agentproto.CertSpec{Mode: "acme"}
	s, e = EgressRules(nodes, groups, nil)
	if e != nil || !strings.Contains(s, "meta mark != { 0x43000001, 0x00000000 } drop") {
		t.Fatal("native ACME traffic broken", e, s)
	}
}
