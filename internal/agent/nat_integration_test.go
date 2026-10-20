package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/nft"
)

// The fixed /etc path and real nft operations are exercised exclusively by
// the network-none disposable container harness, never ordinary local tests.
func TestKernelControllerNAT(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_KERNEL_TEST") != "1" || os.Getenv("CTLVPS_NAT_AGENT_FIXTURE") != "1" {
		t.Skip("isolated Linux agent NAT authority test")
	}
	if _, err := os.Lstat(natconfig.Path); !os.IsNotExist(err) {
		t.Fatal("NAT fixture refuses to replace an existing local authority", err)
	}
	ctx := context.Background()
	a := &Agent{StateDir: t.TempDir(), State: &State{ServerURL: "https://fixture.example", AgentToken: "fixture-only"}, NFT: nft.New(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(func() {
		_ = os.Remove(natconfig.Path)
		_ = a.clearNAT(ctx)
	})
	config := func(local int) *natconfig.Config {
		return &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: local}}}
	}
	ds := &agentproto.DesiredState{Revision: 1, NATVersion: agentproto.NATVersion, NAT: config(10001), Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 30001}}}
	ds.Hash = agentproto.ContentHash(ds)
	apply := func() {
		t.Helper()
		if err := a.syncNATForDesired(ctx, ds, ds.Nodes, nil); err != nil {
			t.Fatal(err)
		}
	}
	assertRule := func(port int) {
		t.Helper()
		raw, err := exec.Command("nft", "list", "table", "inet", nft.NATTable).CombinedOutput()
		if err != nil || !strings.Contains(string(raw), "dport "+strconv.Itoa(port)+" dnat to :30001") {
			t.Fatalf("current authority was not applied: %v %s", err, raw)
		}
	}
	assertStatus := func(source, status string) {
		t.Helper()
		s := a.natDiagnostics()
		if err := agentproto.ValidateNAT(s); err != nil || s.Source != source || s.Status != status {
			t.Fatalf("unexpected NAT report %+v: %v", s, err)
		}
	}
	confirm := func() {
		a.desired = ds
		a.State.AppliedRevision, a.State.AppliedHash = ds.Revision, ds.Hash
	}
	apply()
	assertRule(10001)
	assertStatus("controller", "pending")
	confirm()
	assertStatus("controller", "applied")
	state, err := LoadState(a.StateDir)
	if err != nil || state.NATVersion != agentproto.NATVersion {
		t.Fatal("controller executable requirement was not durable", err)
	}
	ds.Revision++
	ds.NAT = config(10002)
	ds.Hash = agentproto.ContentHash(ds)
	apply()
	assertRule(10002)
	assertStatus("controller", "pending")
	confirm()
	assertStatus("controller", "applied")
	if err := os.MkdirAll(filepath.Dir(natconfig.Path), 0755); err != nil {
		t.Fatal(err)
	}
	writeLocal := func(raw string) {
		t.Helper()
		if err := os.WriteFile(natconfig.Path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeLocal(`{"schema":1,"mappings":[{"public_port":30001,"local_port":10003}]}`)
	apply()
	assertRule(10003)
	assertStatus("local", "applied")
	writeLocal(`{"schema":1,"mappings":[]}`)
	apply()
	assertStatus("local", "disabled")
	if _, err := exec.Command("nft", "list", "table", "inet", nft.NATTable).CombinedOutput(); err == nil {
		t.Fatal("explicit local opt-out retained controller translations")
	}
	writeLocal(`{"schema":999,"mappings":[]}`)
	if err := a.syncNATForDesired(ctx, ds, ds.Nodes, nil); err == nil {
		t.Fatal("malformed local authority fell back to the controller")
	}
	assertStatus("local", "error")
	if err := os.Remove(natconfig.Path); err != nil {
		t.Fatal(err)
	}
	apply()
	assertRule(10002)
	assertStatus("controller", "applied")
	ds.Revision++
	ds.NAT = nil
	ds.Hash = agentproto.ContentHash(ds)
	apply()
	confirm()
	assertStatus("none", "disabled")
	state, err = LoadState(a.StateDir)
	if err != nil || state.NATVersion != agentproto.NATVersion {
		t.Fatal("clearing NAT erased its executable compatibility requirement", err)
	}
	t.Log("PASS actual controller NAT install/replace/remove, receipt status, root priority/opt-out/error and durable v2 requirement")
}
