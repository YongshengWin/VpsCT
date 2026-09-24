package share

import (
	"context"
	"encoding/json"
	"testing"

	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func TestShareLifecyclePreservesNodeNetworkAndImmutableEgress(t *testing.T) {
	m, st, server, _ := setup(t)
	ctx := context.Background()
	sh := &domain.Share{Name: "bound share", Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	if _, err := m.Create(ctx, sh); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	if err != nil || len(nodes) != 1 {
		t.Fatal("missing share node", err)
	}
	original := nodes[0]
	profile := domain.EgressProfile{ServerID: server.ID, Name: "dedicated direct", Kind: "direct", Enabled: true}
	config, _ := json.Marshal(networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}})
	if err := st.CreateEgressProfile(ctx, &profile, config); err != nil {
		t.Fatal(err)
	}
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "override", OnUnavailable: "block", EgressProfileID: profile.ID, EgressRevision: 1}
	host := "independent.example.test"
	if _, err := st.SetNodeNetwork(ctx, original.ID, 0, policy, &host); err != nil {
		t.Fatal(err)
	}
	// Editing a reusable profile must not silently move an existing share.
	if _, err := st.AppendEgressRevision(ctx, profile.ID, 1, "new template", true, config); err != nil {
		t.Fatal(err)
	}
	check := func(revoked, blocked bool) domain.Node {
		t.Helper()
		n, err := st.GetNode(ctx, original.ID)
		if err != nil || n.Network == nil || *n.Network != *policy || n.NetworkRevision != 1 || n.Server != host || n.ShareID == nil || *n.ShareID != sh.ID || n.ListenPort != original.ListenPort || n.Revoked != revoked {
			t.Fatal("share operation changed binding or node identity", err)
		}
		rec, err := st.LatestDesiredState(ctx, server.ID)
		if err != nil {
			t.Fatal(err)
		}
		ds, err := desired.Load(rec)
		if err != nil {
			t.Fatal(err)
		}
		if revoked {
			if len(ds.Nodes) != 0 {
				t.Fatal("revoked node remains in desired state")
			}
		} else if len(ds.Nodes) != 1 || ds.Nodes[0].Network == nil || ds.Nodes[0].Network.Policy.EgressRevision != 1 || ds.Nodes[0].Blocked != blocked {
			t.Fatal("share operation lost desired network or blocking")
		}
		return n
	}
	if err := m.Pause(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	if err := m.Resume(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	check(false, false)
	if err := m.Revoke(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	if _, err := m.Reissue(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	rotated := check(false, false)
	if string(rotated.Params) == string(original.Params) {
		t.Fatal("reissue did not rotate client credentials")
	}
	sh.Targets = nil
	if err := m.EnsureNodes(ctx, sh); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	sh.Targets = []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}
	if err := m.EnsureNodes(ctx, sh); err != nil {
		t.Fatal(err)
	}
	restored := check(false, false)
	if string(restored.Params) == string(rotated.Params) {
		t.Fatal("restoring target reused revoked credentials")
	}
	if err := m.Delete(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteEgressProfile(ctx, profile.ID, 2); err != nil {
		t.Fatal("deleted share left egress references", err)
	}
}
