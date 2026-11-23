package nft

import (
	"context"
	"ctlvps/internal/agentproto"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNodeRulesValidation(t *testing.T) {
	nodes := []agentproto.NodeSpec{{NodeID: 1, ListenPort: 21001, Core: "singbox"}, {NodeID: 2, ListenPort: 21002, Core: "singbox", Blocked: true}}
	rules, err := NodeRules(nodes)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ct mark set meta mark", "ct direction original", "n1_rx", "ct mark 0x43000002 drop"} {
		if !strings.Contains(rules, want) {
			t.Fatal(want)
		}
	}
	nodes[1].ListenPort = 21001
	if _, err := NodeRules(nodes); err == nil {
		t.Fatal("port collision accepted")
	}
	if _, err := NodeMark(1 << 24); err == nil {
		t.Fatal("mark overflow accepted")
	}
}

func TestXrayCountsOnlyClientTCP(t *testing.T) {
	x := agentproto.NodeSpec{NodeID: 9, ListenPort: 24443, Core: "xray", Protocol: "vless"}
	group := "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-n9.slice"
	for _, state := range []string{"active", "blocked", "retired"} {
		x.Blocked, x.Retired = state == "blocked", state == "retired"
		nodes := []agentproto.NodeSpec{x}
		rules, err := NodeRules(nodes)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rules, "add counter inet ctlvps_nodes n9_rx\nadd counter inet ctlvps_nodes n9_tx\n") {
			t.Fatal("lost final meters")
		}
		if strings.Contains(rules, "th dport 24443") || strings.Contains(rules, "udp dport 24443") {
			t.Fatal("charged non-TCP traffic")
		}
		rx := "ct direction original tcp dport 24443 ct mark 0x43000009 counter name n9_rx "
		tx := "ct direction reply tcp sport 24443 ct mark 0x43000009 counter name n9_tx return"
		if strings.Contains(rules, rx+"return") != (state == "active") || strings.Contains(rules, tx) != (state == "active") || strings.Contains(rules, rx+"drop") != (state == "blocked") {
			t.Fatal(state, rules)
		}
		if strings.Contains(rules, "tcp dport 24443 ct mark &") == x.Retired || strings.Contains(rules, "ct mark 0x43000009 counter") && x.Retired {
			t.Fatal("wrong mark lifetime", state)
		}
		egress, err := EgressRules(nodes, map[int64]string{9: group}, []string{"127.0.0.53"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(egress, `ct direction original drop`) != x.Blocked || strings.Contains(egress, "meta mark != 0 drop") != (state == "active") || strings.Contains(egress, "169.254.0.0/16") != (state == "active") {
			t.Fatal(state, egress)
		}
	}
	x.Blocked, x.Retired = false, false
	if _, err := NodeRules([]agentproto.NodeSpec{x, {NodeID: 1, Core: "singbox", ListenPort: 24443}}); err == nil {
		t.Fatal("accepted overlapping meter")
	}
	if _, err := EgressRules([]agentproto.NodeSpec{x}, map[int64]string{9: "/unrelated"}, nil); err == nil {
		t.Fatal("accepted unprotected slice")
	}
	x.AllowPrivate = true
	if _, err := EgressRules([]agentproto.NodeSpec{x}, map[int64]string{9: group}, nil); err == nil {
		t.Fatal("accepted private Xray egress")
	}
}

func TestKernelNodeAccounting(t *testing.T)        { kernelNodeAccounting(t, "singbox") }
func TestKernelXrayIngressAccounting(t *testing.T) { kernelNodeAccounting(t, "xray") }

func kernelNodeAccounting(t *testing.T, core string) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_KERNEL_TEST") != "1" {
		t.Skip("isolated Linux network namespace test")
	}
	dir := t.TempDir()
	nodes := []agentproto.NodeSpec{{NodeID: 1, ListenPort: 21001, Core: core}, {NodeID: 2, ListenPort: 21002, Core: core}}
	for _, name := range []string{"active", "blocked", "retired"} {
		if name == "blocked" {
			nodes[0].Blocked = true
		}
		if name == "retired" {
			nodes[0].Retired = true
		}
		rules, err := NodeRules(nodes)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name+".nft"), []byte(rules), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("python3", "-I", "/work/scripts/meter-test/run.py", dir, core)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
	m := New()
	before, err := m.ReadNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := m.PruneNodes(context.Background(), nodes[1:], []int64{1}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := m.ReadNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].NodeID != 2 || after[0].Rx != before[1].Rx || after[0].Tx != before[1].Tx {
		t.Fatalf("pruning changed active counters: before=%+v after=%+v", before, after)
	}
	// Kernel state disappears across a host reboot, while durable ACK survives.
	if _, err := m.run(context.Background(), "", "delete", "table", "inet", NodeTable); err != nil {
		t.Fatal(err)
	}
	if err := m.PruneNodes(context.Background(), nodes[1:], []int64{1}); err != nil {
		t.Fatal(err)
	}
	after, err = m.ReadNodes(context.Background())
	if err != nil || len(after) != 1 || after[0].NodeID != 2 || after[0].Rx != 0 || after[0].Tx != 0 {
		t.Fatalf("reboot cleanup: %+v %v", after, err)
	}
}

func TestKernelEgressBoundary(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_KERNEL_TEST") != "1" {
		t.Skip("isolated Linux test")
	}
	dir := t.TempDir()
	nodes := []agentproto.NodeSpec{{NodeID: 1, Core: "singbox"}}
	group := "/ctlvps-egress-fixture"
	if e := os.Mkdir("/sys/fs/cgroup"+group, 0755); e != nil {
		t.Fatal(e)
	}
	defer os.Remove("/sys/fs/cgroup" + group)
	rules, e := EgressRules(nodes, map[int64]string{1: group}, nil)
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(dir, "egress.nft")
	if e = os.WriteFile(p, []byte(rules), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("python3", "/work/scripts/meter-test/egress.py", p, group)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	t.Log(string(out))
}
