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
		if strings.Contains(line, "meta mark") && (!strings.Contains(line, "socket cgroupv2") || !strings.HasSuffix(line, " drop")) {
			t.Fatal("mark became authorization", line)
		}
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
