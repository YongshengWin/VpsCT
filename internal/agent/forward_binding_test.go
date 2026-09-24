package agent

import (
	"context"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

func TestForwardBindingKeepsNodeIdentityAndFailedCoreFenced(t *testing.T) {
	r, _, ds := bindingAgentFixture(t)
	f := agentproto.ForwardSpec{ForwardID: ds.Nodes[0].NodeID, Revision: 1, Config: networkconfig.Forward{
		ListenMode: "all", ListenPort: 29990, Network: "tcp", TargetHost: "203.0.113.10", TargetPort: 443,
		SourceMode: "cidr", MaxTCPConnections: 2,
	}}
	ds.Forwards = []agentproto.ForwardSpec{f}
	tx, err := r.prepare(context.Background(), ds, nil)
	if err != nil || len(tx.failures) != 0 || len(tx.forwards) != 1 || len(tx.nodes) != len(ds.Nodes) {
		t.Fatal(tx, err)
	}
	if !tx.staged[forwardResource(f.ForwardID)].Pending || !tx.staged[nodeResource(f.ForwardID)].Pending {
		t.Fatal("unapplied resource has a lease")
	}
	if err := tx.finish(context.Background(), map[string]bool{"singbox": false}); err != nil {
		t.Fatal(err)
	}
	for _, b := range r.plan.Bindings {
		if !b.Pending {
			t.Fatal("failed shared core activated a resource")
		}
	}
	if err := tx.finish(context.Background(), map[string]bool{"singbox": true}); err != nil {
		t.Fatal(err)
	}
	var node, forward bool
	for _, b := range r.plan.Bindings {
		if b.NodeID == f.ForwardID {
			node = !b.Pending
		}
		if b.ForwardID == f.ForwardID {
			forward = !b.Pending
		}
	}
	if !node || !forward {
		t.Fatal("same numeric IDs collided")
	}
	ds.Forwards[0].Revision++
	ds.Forwards[0].Config.TargetPort++
	tx, err = r.prepare(context.Background(), ds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tx.staged[forwardResource(f.ForwardID)].Pending || tx.staged[nodeResource(f.ForwardID)].Pending {
		t.Fatal("target change did not isolate the forward fence")
	}
	// A failure may retain an old process, but never its forward lease.
	if err := tx.finish(context.Background(), map[string]bool{"singbox": false}); err != nil {
		t.Fatal(err)
	}
	for _, b := range r.plan.Bindings {
		if b.ForwardID == f.ForwardID && !b.Pending {
			t.Fatal("rollback revived an old target")
		}
	}
}
