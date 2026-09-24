package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

func impactPeer(t *testing.T, s *Store, sid int64, proto string, enabled bool) domain.Node {
	t.Helper()
	used, err := s.UsedListenPorts(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	port := 23001
	for used[port] {
		port++
	}
	n := domain.Node{Source: domain.NodeDeployed, ServerID: &sid, Name: "peer " + proto, Protocol: proto, Core: domain.CoreFor(proto, domain.CoreModeStable), Server: "fixture.example", Port: port, ListenPort: port, Enabled: enabled, Params: []byte(`{"password":"fixture-private-client"}`), ServerParams: []byte(`{"password":"fixture-private-server"}`)}
	if err := s.CreateNode(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNetworkImpactListsBothSharedGroupsAndKeepsPrivateFieldsOut(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	peer := impactPeer(t, s, *n.ServerID, "vless", true)
	paused := impactPeer(t, s, *n.ServerID, "tuic", false)
	_ = impactPeer(t, s, *n.ServerID, "snell", true)
	foreign, _, _ := egressFixture(t, s)
	_ = impactPeer(t, s, foreign.ID, "ss", true)
	view, err := s.ReviewNodeNetwork(ctx, n.ID, in.Network, nil)
	if err != nil || !view.Ready || view.Impact == nil {
		t.Fatal(view, err)
	}
	v := view.Impact
	if len(v.Nodes) != 3 || v.ReferenceCount != 1 || v.RestartCount != 3 || v.RestartScope != "server_singbox" {
		t.Fatal(v)
	}
	if v.Nodes[1].NodeID != peer.ID || v.Nodes[1].Effect != "restart" || v.Nodes[2].NodeID != paused.ID || !v.Nodes[2].Blocked {
		t.Fatal(v.Nodes)
	}
	wire, _ := json.Marshal(v)
	for _, secret := range []string{"fixture-private-client", "fixture-private-server", "server_params", "password"} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("impact leaked %q", secret)
		}
	}
	// Ordinary observation does not invalidate a review of the same scope.
	if _, err = s.db.Exec(`UPDATE agents SET last_seen_at=? WHERE server_id=?`, fmtTime(s.Now()), *n.ServerID); err != nil {
		t.Fatal(err)
	}
	again, err := s.ReviewNodeNetwork(ctx, n.ID, in.Network, nil)
	if err != nil || again.Impact.Token != v.Token {
		t.Fatal("heartbeat changed impact", err)
	}
}

func TestNetworkImpactRejectsNewPeerAtomicallyAndAcceptedReplayStillWorks(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	if _, err := s.RequestReviewedNodeNetwork(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactRequired) {
		t.Fatal(err)
	}
	view, err := s.ReviewNodeNetwork(ctx, n.ID, in.Network, nil)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedImpact = view.Impact.Token
	_ = impactPeer(t, s, *n.ServerID, "vless", true)
	if _, err = s.RequestReviewedNodeNetwork(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactChanged) {
		t.Fatal(err)
	}
	got, _ := s.GetNode(ctx, n.ID)
	if got.Network != nil || got.NetworkRevision != 0 {
		t.Fatal("conflict partially changed binding")
	}
	if _, err = s.NetworkOperation(ctx, in.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("conflict left operation", err)
	}
	view, err = s.ReviewNodeNetwork(ctx, n.ID, in.Network, nil)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedImpact = view.Impact.Token
	op, err := s.RequestReviewedNodeNetwork(ctx, in, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	_ = impactPeer(t, s, *n.ServerID, "trojan", true)
	if _, err = s.db.Exec(`UPDATE agents SET last_seen_at=NULL WHERE server_id=?`, *n.ServerID); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.RequestReviewedNodeNetwork(ctx, in, domain.AuditEvent{}); err != nil || replay.ID != op.ID || replay.ResourceRevision != 1 {
		t.Fatal("accepted retry lost identity", err)
	}
	// A changed candidate cannot reuse the token or its accepted operation ID.
	changed := in
	changed.ID = strings.Repeat("2", 32)
	changed.ExpectedRevision = 1
	changed.Network = nil
	if _, err = s.RequestReviewedNodeNetwork(ctx, changed, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactChanged) {
		t.Fatal(err)
	}
}

func TestEgressImpactProtectsConsumersAndOldPinnedVersions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	if _, err := s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetEgressProfile(ctx, in.Network.EgressProfileID)
	edit := EgressProfileRequest{ID: strings.Repeat("3", 32), Action: "update", ProfileID: p.ID, ExpectedRevision: 1, Name: p.Name, Kind: "direct", Enabled: false, Config: directFixture("192.0.2.54")}
	view, err := s.PreviewEgressProfile(ctx, edit)
	if err != nil || !view.Ready || !view.Impact.RuntimeChange || view.Impact.ReferenceCount != 1 || view.Impact.Nodes[0].EgressRevision != 1 || view.Impact.Nodes[0].Effect != "disable" {
		t.Fatal(view, err)
	}
	edit.ExpectedImpact = view.Impact.Token
	peer := impactPeer(t, s, p.ServerID, "ss", false)
	peer.ServerParams = []byte(`{"method":"2022-blake3-aes-128-gcm"}`)
	if err = s.UpdateNodeCredentials(ctx, &peer); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetNodeNetwork(ctx, peer.ID, 0, in.Network, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := s.NetworkGeneration(ctx, p.ServerID)
	if _, err = s.RequestReviewedEgressProfile(ctx, edit, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactChanged) {
		t.Fatal(err)
	}
	got, _ := s.GetEgressProfile(ctx, p.ID)
	if !got.Enabled || got.CurrentRevision != 1 {
		t.Fatal("conflict partially stopped egress")
	}
	if generation, _ := s.NetworkGeneration(ctx, p.ServerID); generation != before {
		t.Fatal("conflict published")
	}
	view, err = s.PreviewEgressProfile(ctx, edit)
	if err != nil || len(view.Impact.Nodes) != 2 || !view.Impact.Nodes[1].Blocked {
		t.Fatal(view, err)
	}
	edit.ExpectedImpact = view.Impact.Token
	if _, err = s.RequestReviewedEgressProfile(ctx, edit, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	// Enabling previews the old pinned versions, even for paused consumers.
	edit.ID, edit.ExpectedRevision, edit.Enabled = strings.Repeat("4", 32), 2, true
	view, err = s.PreviewEgressProfile(ctx, edit)
	if err != nil || !view.Ready || view.Impact.Nodes[1].EgressRevision != 1 {
		t.Fatal(view, err)
	}
	if _, err = s.db.Exec(`UPDATE network_snapshots SET received_at='2000-01-01T00:00:00Z' WHERE server_id=?`, p.ServerID); err != nil {
		t.Fatal(err)
	}
	edit.ExpectedImpact = view.Impact.Token
	if _, err = s.RequestReviewedEgressProfile(ctx, edit, domain.AuditEvent{}); !errors.Is(err, ErrNetworkNotReady) {
		t.Fatal("review bypassed fresh readiness", err)
	}
	view, err = s.PreviewEgressProfile(ctx, edit)
	if err != nil || view.Ready || len(view.Issues) == 0 {
		t.Fatal(view, err)
	}
}

func TestEgressImpactUnreferencedPreviewCannotStopNewConsumer(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	p, _ := s.GetEgressProfile(ctx, in.Network.EgressProfileID)
	edit := EgressProfileRequest{ID: strings.Repeat("5", 32), Action: "update", ProfileID: p.ID, ExpectedRevision: 1, Name: p.Name, Kind: "direct", Enabled: false, Config: directFixture("192.0.2.53")}
	v, err := s.PreviewEgressProfile(ctx, edit)
	if err != nil || v.Impact.RuntimeChange || len(v.Impact.Nodes) != 0 {
		t.Fatal(v, err)
	}
	edit.ExpectedImpact = v.Impact.Token
	if _, err = s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestReviewedEgressProfile(ctx, edit, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactChanged) {
		t.Fatal("new consumer stopped without review", err)
	}
}

func TestNetworkImpactCapacityDoesNotSilentlyTruncate(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	// Only this test bypasses normal provisioning to exercise the exact bound.
	_, err := s.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x<?)
 INSERT INTO nodes(name,protocol,server,port,source,server_id,listen_port,core,enabled,created_at,updated_at)
 SELECT 'capacity-'||x,'ss','fixture.example',24000+x,'deployed',?,24000+x,'singbox',1,'2026-09-20T00:00:00Z','2026-09-20T00:00:00Z' FROM seq`, MaxNetworkImpactNodes, *n.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReviewNodeNetwork(ctx, n.ID, in.Network, nil); !errors.Is(err, ErrNetworkImpactCapacity) {
		t.Fatal(fmt.Sprint(err))
	}
}

func TestNetworkImpactIncludesAppliedAndPartiallyAppliedHistoricalNodes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	create := func(nodes ...agentproto.NodeSpec) domain.DesiredState {
		t.Helper()
		b, _ := json.Marshal(agentproto.DesiredState{ServerID: *n.ServerID, Nodes: nodes})
		d, err := s.CreateDesiredState(ctx, *n.ServerID, b, strings.Repeat("b", 64))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	old := agentproto.NodeSpec{NodeID: 10001, Name: "removed from node library", Protocol: "ss", Core: "singbox", ListenPort: 21111, Params: map[string]any{"password": "historical-private-password"}}
	applied := create(old)
	if _, err := s.db.Exec(`UPDATE agents SET applied_revision=?,applied_hash=? WHERE server_id=?`, applied.Revision, applied.Hash, *n.ServerID); err != nil {
		t.Fatal(err)
	}
	partial := old
	partial.NodeID = 10002
	partial.Name = "partial apply"
	partial.ListenPort = 21112
	failed := create(partial)
	if err := s.MarkDesiredState(ctx, *n.ServerID, failed.Revision, domain.DesiredFailed, "private error"); err != nil {
		t.Fatal(err)
	}
	create()
	v, err := s.ReviewNodeNetwork(ctx, n.ID, in.Network, nil)
	if err != nil || !v.Impact.HistoryComplete || len(v.Impact.HistoricalNodes) != 2 {
		t.Fatal(v, err)
	}
	wire, _ := json.Marshal(v)
	if strings.Contains(string(wire), "historical-private-password") || strings.Contains(string(wire), "private error") {
		t.Fatal("historical secrets leaked")
	}
	in.ExpectedImpact = v.Impact.Token
	newer := partial
	newer.NodeID = 10003
	newer.ListenPort = 21113
	create(newer)
	if _, err = s.RequestReviewedNodeNetwork(ctx, in, domain.AuditEvent{}); !errors.Is(err, ErrNetworkImpactChanged) {
		t.Fatal("new potential running node did not invalidate review", err)
	}
	if err = s.PruneDesiredStates(ctx, 1); err != nil {
		t.Fatal(err)
	}
	v, err = s.ReviewNodeNetwork(ctx, n.ID, in.Network, nil)
	if err != nil || v.Impact.HistoryComplete || len(v.Impact.HistoricalNodes) != 1 {
		t.Fatal("pruned history pretended complete", v, err)
	}
}

func TestNetworkImpactConcurrentDisableAndNewBindingCannotBothCommit(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	if _, err := s.RequestReadyNodeNetwork(ctx, in, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetEgressProfile(ctx, in.Network.EgressProfileID)
	peer := impactPeer(t, s, p.ServerID, "ss", true)
	edit := EgressProfileRequest{ID: strings.Repeat("6", 32), Action: "update", ProfileID: p.ID, ExpectedRevision: 1, Name: p.Name, Kind: "direct", Enabled: false, Config: directFixture("192.0.2.53")}
	v, err := s.PreviewEgressProfile(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	edit.ExpectedImpact = v.Impact.Token
	start := make(chan struct{})
	disabled := make(chan error, 1)
	bound := make(chan error, 1)
	go func() {
		<-start
		_, err := s.RequestReviewedEgressProfile(ctx, edit, domain.AuditEvent{})
		disabled <- err
	}()
	go func() { <-start; _, err := s.SetNodeNetwork(ctx, peer.ID, 0, in.Network, nil); bound <- err }()
	close(start)
	disableErr, bindingErr := <-disabled, <-bound
	if disableErr == nil && bindingErr == nil {
		t.Fatal("new consumer was disabled without appearing in reviewed scope")
	}
	if disableErr != nil && !errors.Is(disableErr, ErrNetworkImpactChanged) && !errors.Is(disableErr, ErrNetworkConflict) {
		t.Fatal(disableErr)
	}
}
