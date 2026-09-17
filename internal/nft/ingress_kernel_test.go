package nft

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
)

// Run only in a disposable network namespace; never on a production host.
func TestKernelIngressLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_INGRESS_TEST") != "1" {
		t.Skip("isolated Linux nftables test")
	}
	ctx := context.Background()
	m := New()
	seed := `add table inet filter
add chain inet filter input { type filter hook input priority 0; policy drop; }
add rule inet filter input tcp dport 22 accept comment "keep-ssh"
add rule inet filter input counter drop comment "keep-drop"
`
	if _, err := m.run(ctx, seed, "-f", "-"); err != nil {
		t.Fatal(err)
	}
	defer m.run(ctx, "", "delete", "table", "inet", "filter")
	nodes := []agentproto.NodeSpec{{Protocol: "anytls", ListenPort: 23456}, {Protocol: "hysteria2", ListenPort: 23457}}
	snapshot := func() []byte {
		b, e := m.run(ctx, "", "-j", "list", "ruleset")
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	for step := 0; step < 3; step++ {
		if err := m.EnsureIngress(ctx, nodes); err != nil {
			t.Fatal(err)
		}
		b := snapshot()
		if rules, err := IngressRules(b, nodes); err != nil || rules != "" {
			t.Fatalf("not idempotent: %s %v", rules, err)
		}
		if !strings.Contains(string(b), "keep-ssh") || !strings.Contains(string(b), "keep-drop") {
			t.Fatal("administrator rules lost")
		}
		switch step {
		case 0:
			// A firewall reload removes generated rules; the next reconciliation restores them.
			if _, err := m.run(ctx, "flush chain inet filter input\nadd rule inet filter input tcp dport 22 accept comment \"keep-ssh\"\nadd rule inet filter input drop comment \"keep-drop\"\n", "-f", "-"); err != nil {
				t.Fatal(err)
			}
		case 1:
			nodes[0].ListenPort = 23458
			nodes[1].Blocked = true
		}
	}
	if err := m.EnsureIngress(ctx, nil); err != nil {
		t.Fatal(err)
	}
	b := string(snapshot())
	if strings.Contains(b, ingressComment) || !strings.Contains(b, "keep-ssh") || !strings.Contains(b, "keep-drop") {
		t.Fatal("incorrect cleanup")
	}
}
