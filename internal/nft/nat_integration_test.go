package nft

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/networkconfig"
)

// TestKernelNAT runs only in the disposable privileged meter-test container.
// Native echo sockets exercise the same netfilter/conntrack path as proxy
// listeners without requiring a downloaded or modified proxy core.
func TestKernelNAT(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_KERNEL_TEST") != "1" {
		t.Skip("isolated Linux NAT network namespace test")
	}
	dir := t.TempDir()
	nodes := []agentproto.NodeSpec{
		{NodeID: 1, ListenPort: 30001, Core: "singbox", Protocol: "ss"},
		{NodeID: 2, ListenPort: 30002, Core: "singbox", Protocol: "ss"},
	}
	forwards := []agentproto.ForwardSpec{
		{ForwardID: 1, Config: networkconfig.Forward{ListenPort: 31001, Network: "both"}},
		{ForwardID: 2, Config: networkconfig.Forward{ListenPort: 31002, Network: "both"}},
	}
	cfg := natconfig.Config{Schema: 1, Interface: "v-nat", Mappings: []natconfig.Mapping{
		{PublicPort: 30001, LocalPort: 10001, Count: 3},
		{PublicPort: 31001, LocalPort: 11001, Count: 2},
	}}
	for _, state := range []string{"active", "blocked", "retired"} {
		if state == "blocked" {
			nodes[0].Blocked = true
			forwards[0].Blocked = true
		}
		if state == "retired" {
			nodes[0].Retired = true
			forwards[0].Retired = true
		}
		meters, err := NodeRules(nodes)
		if err != nil {
			t.Fatal(err)
		}
		forwardMeters := make([]agentproto.ForwardMeterRule, len(forwards))
		for i, f := range forwards {
			forwardMeters[i] = agentproto.ForwardMeterRule{ForwardID: f.ForwardID, ListenPort: f.Config.ListenPort, Blocked: f.Blocked, Retired: f.Retired}
		}
		forwardRules, err := ForwardRules(forwardMeters)
		if err != nil {
			t.Fatal(err)
		}
		translations, err := NATRules(cfg, nodes, forwards)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, state+".nft"), []byte(meters+forwardRules+translations), 0600); err != nil {
			t.Fatal(err)
		}
		if state == "active" {
			if err := os.WriteFile(filepath.Join(dir, "nodes-active.nft"), []byte(meters), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	cmd := exec.Command("python3", "/work/scripts/meter-test/nat.py", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("NAT kernel verification failed: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
