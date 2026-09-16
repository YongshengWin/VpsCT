package nft

import (
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

func TestKernelNodeAccounting(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_KERNEL_TEST") != "1" {
		t.Skip("isolated Linux network namespace test")
	}
	dir := t.TempDir()
	nodes := []agentproto.NodeSpec{{NodeID: 1, ListenPort: 21001, Core: "singbox"}, {NodeID: 2, ListenPort: 21002, Core: "singbox"}}
	for _, name := range []string{"active", "blocked"} {
		if name == "blocked" {
			nodes[0].Blocked = true
		}
		rules, err := NodeRules(nodes)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name+".nft"), []byte(rules), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("python3", "/work/scripts/meter-test/run.py", dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
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
