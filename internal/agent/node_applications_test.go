package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
)

type applicationFinalizer struct {
	core.Driver
	calls   []bool
	err     error
	running bool
}

func (d *applicationFinalizer) FinishActivation(_ context.Context, success bool) error {
	d.calls = append(d.calls, success)
	d.running = true // A successful rollback can restore the previous service.
	return d.err
}

func (d *applicationFinalizer) Stop(context.Context) error { d.running = false; return nil }

func TestGuardFailureCannotBeUndoneByCoreRollback(t *testing.T) {
	d := &applicationFinalizer{}
	a := &Agent{StateDir: t.TempDir(), State: &State{}, Drivers: map[string]core.Driver{"singbox": d}}
	ds := &agentproto.DesiredState{Revision: 2, Hash: strings.Repeat("a", 64), Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", Network: &agentproto.NodeNetworkSpec{}}}}
	if err := a.finishApplications(ds, ds.Nodes, map[string]bool{}, map[string]bool{"singbox": true}, true, false); err != nil {
		t.Fatal(err)
	}
	if d.running || len(d.calls) != 1 || d.calls[0] {
		t.Fatal("rollback reopened service after admission failed")
	}
	if len(a.State.NodeApplications) != 1 || a.State.NodeApplications[0].Status != "failed" {
		t.Fatal("guard-stopped node retained applied proof")
	}
}

func TestApplicationFinalizesBeforeDurablePartialReceipt(t *testing.T) {
	sb, snell, retained := &applicationFinalizer{}, &applicationFinalizer{}, &applicationFinalizer{}
	a := &Agent{StateDir: t.TempDir(), State: &State{ServerURL: "https://fixture.example.test", AgentToken: "fixture", ServerID: 1}, Drivers: map[string]core.Driver{"singbox": sb, "snell": snell, "mieru": retained}}
	ds := &agentproto.DesiredState{Revision: 4, Hash: strings.Repeat("a", 64), Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox"}, {NodeID: 2, Core: "snell"}, {NodeID: 3, Core: "mieru"}, {NodeID: 4, Core: "singbox", Blocked: true}}}
	if err := a.finishNodeApplications(ds, ds.Nodes, map[string]bool{"singbox": true}, map[string]bool{"singbox": true, "snell": true}); err != nil {
		t.Fatal(err)
	}
	if len(sb.calls) != 1 || !sb.calls[0] || len(snell.calls) != 1 || snell.calls[0] {
		t.Fatal("successful and failed cores did not finalize independently")
	}
	if len(retained.calls) != 0 {
		t.Fatal("installation waiting with verified old service was rolled back")
	}
	st, err := LoadState(a.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.AppliedRevision != 0 || st.NodeApplicationRevision != 4 || st.NodeApplicationSequence != 1 || len(st.NodeApplications) != 3 {
		t.Fatal("partial success became a global success or was not durable")
	}
	statuses := map[int64]string{}
	for _, r := range st.NodeApplications {
		statuses[r.NodeID] = r.Status
	}
	if statuses[1] != "applied" || statuses[2] != "failed" || statuses[4] != "blocked" || statuses[3] != "" {
		t.Fatal("partial receipts include an uncommitted/retained resource")
	}
}

func TestApplicationFinalizationFailureCannotAdvertiseSuccess(t *testing.T) {
	d := &applicationFinalizer{err: errors.New("commit unavailable")}
	a := &Agent{StateDir: t.TempDir(), State: &State{}, Drivers: map[string]core.Driver{"singbox": d}}
	ds := &agentproto.DesiredState{Revision: 2, Hash: strings.Repeat("b", 64), Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox"}}}
	if err := a.finishNodeApplications(ds, ds.Nodes, map[string]bool{"singbox": true}, map[string]bool{"singbox": true}); err == nil {
		t.Fatal("failed activation commit was ignored")
	}
	if len(a.State.NodeApplications) != 1 || a.State.NodeApplications[0].Status != "failed" {
		t.Fatal("uncommitted core was reported applied")
	}
	if d.running {
		t.Fatal("uncertain rollback left an uncommitted service running")
	}
}

func TestApplicationDownloadWaitKeepsPriorReceiptAndRecoveryPoint(t *testing.T) {
	d := &applicationFinalizer{}
	st := &State{NodeApplicationRevision: 1, NodeApplicationHash: strings.Repeat("a", 64), NodeApplicationSequence: 7, NodeApplications: []agentproto.NodeApplication{{NodeID: 1, Core: "singbox", Status: "applied"}}}
	a := &Agent{StateDir: t.TempDir(), State: st, Drivers: map[string]core.Driver{"singbox": d}}
	ds := &agentproto.DesiredState{Revision: 2, Hash: strings.Repeat("b", 64), Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox"}}}
	if err := a.finishNodeApplications(ds, ds.Nodes, map[string]bool{}, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 0 || st.NodeApplicationRevision != 1 || st.NodeApplicationSequence != 7 || st.NodeApplications[0].Status != "applied" {
		t.Fatal("untouched service or its application proof was lost while downloading")
	}
}

func TestApplicationReceiptPersistenceFailureSuppressesUnsafeProof(t *testing.T) {
	blockedDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedDir, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{StateDir: blockedDir, State: &State{NodeApplicationSequence: 8}, Drivers: map[string]core.Driver{"singbox": &applicationFinalizer{}}}
	ds := &agentproto.DesiredState{Revision: 2, Hash: strings.Repeat("c", 64), Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox"}}}
	if err := a.finishNodeApplications(ds, ds.Nodes, map[string]bool{"singbox": true}, map[string]bool{"singbox": true}); err == nil {
		t.Fatal("undurable receipt was accepted")
	}
	if len(a.State.NodeApplications) != 0 || a.State.NodeApplicationRevision != 0 || a.State.NodeApplicationSequence != 8 {
		t.Fatal("heartbeat may advertise an undurable application proof")
	}
}
