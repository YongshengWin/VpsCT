package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

func applicationNodes(t *testing.T, s *Store) (domain.Server, []domain.Node) {
	t.Helper()
	ctx := context.Background()
	sv := domain.Server{Name: "fixture", PublicHost: "fixture.example.test", Enabled: true, CoreMode: domain.CoreModeStable, CertMode: "self_signed"}
	var version int
	if err := s.db.QueryRow(`SELECT max(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 37 {
		historicalCreateServer(t, s, &sv)
	} else if err := s.CreateServer(ctx, &sv); err != nil {
		t.Fatal(err)
	}
	nodes := []domain.Node{
		{Name: "first", Protocol: "ss", Server: sv.PublicHost, Port: 21001, Params: json.RawMessage(`{"cipher":"aes-128-gcm","password":"fixture-only-secret"}`), ServerParams: json.RawMessage(`{"method":"aes-128-gcm","password":"fixture-only-secret"}`), Source: domain.NodeDeployed, ServerID: &sv.ID, ListenPort: 21001, Core: domain.CoreSingBox, Enabled: true},
		{Name: "second", Protocol: "snell", Server: sv.PublicHost, Port: 21002, Params: json.RawMessage(`{"psk":"fixture-only-secret","version":5}`), ServerParams: json.RawMessage(`{"psk":"fixture-only-secret","version":5}`), Source: domain.NodeDeployed, ServerID: &sv.ID, ListenPort: 21002, Core: domain.CoreSnell, Enabled: true},
	}
	for i := range nodes {
		if err := s.CreateNode(ctx, &nodes[i]); err != nil {
			t.Fatal(err)
		}
	}
	return sv, nodes
}

func applicationPublish(t *testing.T, s *Store, sv domain.Server, nodes []domain.Node, letter string) domain.DesiredState {
	t.Helper()
	ctx := context.Background()
	gen, err := s.NetworkGeneration(ctx, sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	wanted := &agentproto.DesiredState{ServerID: sv.ID, PublicHost: sv.PublicHost, CoreMode: string(sv.CoreMode), IPv4Only: sv.IPv4Only, NetworkGeneration: gen, Hash: strings.Repeat(letter, 64)}
	for _, n := range nodes {
		var params map[string]any
		if err := json.Unmarshal(n.ServerParams, &params); err != nil {
			t.Fatal(err)
		}
		wanted.Nodes = append(wanted.Nodes, agentproto.NodeSpec{NodeID: n.ID, Name: n.Name, Protocol: n.Protocol, Core: string(n.Core), ListenPort: n.ListenPort, ShareID: n.ShareID, Params: params, Blocked: !n.Enabled})
	}
	latest, _ := s.LatestDesiredState(ctx, sv.ID)
	rec, _, err := s.PublishDesired(ctx, latest.Revision, wanted, false)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestApplicationReceiptsKeepOfflineNodesAndFenceRotations(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	rec := applicationPublish(t, s, sv, nodes, "a")
	assert := func(n domain.Node, want bool) {
		t.Helper()
		_, ok, err := s.AppliedSubscriptionNode(ctx, n)
		if err != nil || ok != want {
			t.Fatalf("usable=%v want=%v err=%v", ok, want, err)
		}
	}
	assert(nodes[0], false)
	partial := []agentproto.NodeApplication{{NodeID: nodes[0].ID, Core: "singbox", Status: "applied"}, {NodeID: nodes[1].ID, Core: "snell", Status: "failed"}}
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "failed", partial, 1); err != nil {
		t.Fatal(err)
	}
	assert(nodes[0], true)
	assert(nodes[1], false)
	// There is no last-seen timestamp or online agent in the fixture. Confirmed
	// resources survive liveness loss and pruning of their original desired row.
	var encrypted string
	if err := s.db.QueryRow(`SELECT payload FROM node_applied_states WHERE node_id=?`, nodes[0].ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, secretPrefix) || strings.Contains(encrypted, "fixture-only-secret") {
		t.Fatal("client snapshot was stored in plaintext")
	}
	nodes[0].Params = json.RawMessage(`{"cipher":"aes-128-gcm","password":"rotated-fixture-secret"}`)
	nodes[0].ServerParams = json.RawMessage(`{"method":"aes-128-gcm","password":"rotated-fixture-secret"}`)
	if err := s.UpdateNodeCredentials(ctx, &nodes[0]); err != nil {
		t.Fatal(err)
	}
	assert(nodes[0], false)
	updated := applicationPublish(t, s, sv, nodes, "b")
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", nil, 0); err != nil {
		t.Fatal(err)
	}
	assert(nodes[0], false)
	if err := s.RecordNodeApplications(ctx, sv.ID, updated.Revision, updated.Hash, "applied", nil, 0); err != nil {
		t.Fatal(err)
	}
	assert(nodes[0], true)
	if err := s.PruneDesiredStates(ctx, 1); err != nil {
		t.Fatal(err)
	}
	assert(nodes[0], true)
	nodes[0].Enabled = false
	if err := s.UpdateNode(ctx, &nodes[0]); err != nil {
		t.Fatal(err)
	}
	assert(nodes[0], false)
}

func TestApplicationSequenceRejectsStaleSuccessAndWrongServer(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	rec := applicationPublish(t, s, sv, nodes, "a")
	receipt := []agentproto.NodeApplication{{NodeID: nodes[0].ID, Core: "singbox", Status: "applied"}}
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", receipt, 1); err != nil {
		t.Fatal(err)
	}
	receipt[0].Status = "blocked"
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "failed", receipt, 2); err != nil {
		t.Fatal(err)
	}
	receipt[0].Status = "applied"
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", receipt, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.AppliedSubscriptionNode(ctx, nodes[0]); err != nil || ok {
		t.Fatal("old success reopened a newer fence", err)
	}
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", receipt, 3); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.AppliedSubscriptionNode(ctx, nodes[0]); err != nil || !ok {
		t.Fatal("new confirmed recovery failed", err)
	}
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, strings.Repeat("f", 64), "applied", receipt, 4); err == nil {
		t.Fatal("wrong hash accepted")
	}
	receipt[0].Core = "snell"
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", receipt, 4); err == nil {
		t.Fatal("wrong core accepted")
	}
}

func TestApplicationSnapshotBindsClientAndServerContext(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	rec := applicationPublish(t, s, sv, nodes, "a")
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", nil, 0); err != nil {
		t.Fatal(err)
	}
	original := nodes[0]
	nodes[0].Params = json.RawMessage(`{"cipher":"aes-128-gcm","password":"fixture-only-secret","sni":"changed.example.test"}`)
	if err := s.UpdateNode(ctx, &nodes[0]); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.AppliedSubscriptionNode(ctx, nodes[0]); err != nil || ok {
		t.Fatal("unconfirmed client parameters escaped", err)
	}
	if err := s.UpdateNode(ctx, &original); err != nil {
		t.Fatal(err)
	}
	sv.PublicHost = "changed.example.test"
	if err := s.UpdateServer(ctx, &sv); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.AppliedSubscriptionNode(ctx, original); err != nil || ok {
		t.Fatal("unconfirmed hostname escaped", err)
	}
}

func TestApplicationBackfillUsesExactHistoricReceipt(t *testing.T) {
	s, path := historicalStore(t, 32)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	// Old publications have no candidate/client snapshot table. Construct the
	// released desired payload, then keep the agent offline through reopening.
	wanted := agentproto.DesiredState{ServerID: sv.ID, PublicHost: sv.PublicHost, CoreMode: string(sv.CoreMode)}
	for _, n := range nodes {
		var params map[string]any
		_ = json.Unmarshal(n.ServerParams, &params)
		wanted.Nodes = append(wanted.Nodes, agentproto.NodeSpec{NodeID: n.ID, Protocol: n.Protocol, ListenPort: n.ListenPort, Core: string(n.Core), Params: params})
	}
	payload, _ := json.Marshal(wanted)
	rec, err := s.CreateDesiredState(ctx, sv.ID, payload, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET applied_revision=?,applied_hash=? WHERE server_id=?`, rec.Revision, rec.Hash, sv.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	for _, n := range nodes {
		if _, ok, err := current.AppliedSubscriptionNode(ctx, n); err != nil || !ok {
			t.Fatal("upgrade removed confirmed offline node", err)
		}
	}
	if _, err := current.db.Exec(`DELETE FROM node_applied_states; UPDATE agents SET applied_hash=?`, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if err := current.BackfillNodeApplications(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := current.AppliedSubscriptionNode(ctx, nodes[0]); err != nil || ok {
		t.Fatal("inexact historical receipt trusted", err)
	}
}

func TestOrdinaryMutationPublicationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	first := applicationPublish(t, s, sv, nodes, "a")
	if pending, err := s.PendingNetworkPublications(ctx); err != nil || len(pending) != 0 {
		t.Fatal("publication was not committed atomically", err)
	}
	nodes[0].Enabled = false
	if err := s.UpdateNode(ctx, &nodes[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pending, err := s.PendingNetworkPublications(ctx)
	if err != nil || len(pending) != 1 || pending[0].Generation <= pending[0].PublishedGeneration {
		t.Fatal("ordinary mutation lost durable outbox", err)
	}
	if first.Revision != 1 {
		t.Fatal(first.Revision)
	}
	if version, err := s.NetworkBindingVersion(ctx, sv.ID); err != nil || version != 0 {
		t.Fatal("ordinary mutation opted into binding protocol", version, err)
	}
}

func TestFailedApplicationNeverAdvancesActualAgentRevision(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	first := applicationPublish(t, s, sv, nodes, "a")
	ag, err := s.GetAgentByServer(ctx, sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAgentApplication(ctx, ag.ID, sv.ID, first.Revision, first.Hash, "applied", ""); err != nil {
		t.Fatal(err)
	}
	second := applicationPublish(t, s, sv, nodes, "b")
	if err := s.RecordAgentApplication(ctx, ag.ID, sv.ID, second.Revision, second.Hash, "failed", "activation fixture failure"); err != nil {
		t.Fatal(err)
	}
	ag, err = s.GetAgentByServer(ctx, sv.ID)
	if err != nil || ag.AppliedRevision != first.Revision || ag.AppliedHash != first.Hash || ag.ApplyError == "" {
		t.Fatal("failed candidate replaced actual application proof", err)
	}
	if err := s.RecordAgentApplication(ctx, ag.ID, sv.ID, second.Revision, strings.Repeat("f", 64), "applied", ""); err == nil {
		t.Fatal("forged global hash accepted")
	}
	if err := s.RecordAgentApplication(ctx, ag.ID, sv.ID, second.Revision, second.Hash, "applied", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAgentApplication(ctx, ag.ID, sv.ID, first.Revision, first.Hash, "failed", "stale error"); !errors.Is(err, ErrStaleApplication) {
		t.Fatal(err)
	}
	ag, err = s.GetAgentByServer(ctx, sv.ID)
	if err != nil || ag.AppliedRevision != second.Revision || ag.AppliedHash != second.Hash || ag.ApplyError != "" {
		t.Fatal("old report undid newer success", err)
	}
}

func TestStaleSuccessCannotClearNewerResourceFailure(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	rec := applicationPublish(t, s, sv, nodes, "a")
	ag, _ := s.GetAgentByServer(ctx, sv.ID)
	receipt := []agentproto.NodeApplication{{NodeID: nodes[0].ID, Core: "singbox", Status: "applied"}}
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "applied", receipt, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAgentApplication(ctx, ag.ID, sv.ID, rec.Revision, rec.Hash, "applied", "", 1); err != nil {
		t.Fatal(err)
	}
	receipt[0].Status = "failed"
	if err := s.RecordNodeApplications(ctx, sv.ID, rec.Revision, rec.Hash, "failed", receipt, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAgentApplication(ctx, ag.ID, sv.ID, rec.Revision, rec.Hash, "failed", "guard fixture failure", 2); err != nil {
		t.Fatal(err)
	}
	for _, sequence := range []int64{0, 1, 2} {
		if err := s.RecordAgentApplication(ctx, ag.ID, sv.ID, rec.Revision, rec.Hash, "applied", "", sequence); !errors.Is(err, ErrStaleApplication) {
			t.Fatal("stale success cleared resource failure", sequence, err)
		}
	}
	ag, err := s.GetAgentByServer(ctx, sv.ID)
	if err != nil || ag.ApplyError == "" {
		t.Fatal("failure was lost", err)
	}
}

func TestChainOriginRequiresUniqueMatchingDecryptedProjection(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	wrong := nodes[0]
	wrong.ID, wrong.Source, wrong.ServerID = 0, domain.NodeManual, nil
	wrong.Params = json.RawMessage(`{"cipher":"aes-128-gcm","password":"different-fixture"}`)
	if err := s.CreateNode(ctx, &wrong); err != nil {
		t.Fatal(err)
	}
	chain := nodes[0]
	chain.ID, chain.Source, chain.ServerID = 0, domain.NodeChain, nil
	if err := s.CreateNode(ctx, &chain); err != nil {
		t.Fatal(err)
	}
	landing, ok, err := s.ChainLanding(ctx, chain.ID)
	if err != nil || !ok || landing.ID != nodes[0].ID {
		t.Fatal("different credentials hijacked chain origin", err)
	}
	duplicate := nodes[0]
	duplicate.ID, duplicate.Source, duplicate.ServerID = 0, domain.NodeManual, nil
	sh := domain.Share{Name: "ambiguous", Status: domain.ShareActive}
	if err := s.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	duplicate.ShareID = &sh.ID
	if err := s.CreateNode(ctx, &duplicate); err != nil {
		t.Fatal(err)
	}
	ambiguous := chain
	ambiguous.ID = 0
	if err := s.CreateNode(ctx, &ambiguous); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ChainLanding(ctx, ambiguous.ID); err != nil || ok {
		t.Fatal("ambiguous same credential/endpoint chose a different share identity", err)
	}
	if err := s.DeleteNode(ctx, nodes[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ChainLanding(ctx, chain.ID); err != nil || ok {
		t.Fatal("deleted origin rebound to replacement", err)
	}
	_ = sv
}

func TestPrunedApplicationHistoryStillAcceptsResourceFailureFences(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	first := applicationPublish(t, s, sv, nodes, "a")
	receipt := []agentproto.NodeApplication{{NodeID: nodes[0].ID, Core: "singbox", Status: "applied"}}
	if err := s.RecordNodeApplications(ctx, sv.ID, first.Revision, first.Hash, "applied", receipt, 1); err != nil {
		t.Fatal(err)
	}
	applicationPublish(t, s, sv, nodes, "b")
	if err := s.PruneDesiredStates(ctx, 1); err != nil {
		t.Fatal(err)
	}
	receipt[0].Status = "blocked"
	if err := s.RecordNodeApplications(ctx, sv.ID, first.Revision, first.Hash, "failed", receipt, 2); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.AppliedSubscriptionNode(ctx, nodes[0]); err != nil || ok {
		t.Fatal("history pruning froze an old successful application", err)
	}
	receipt[0].Status = "applied"
	if err := s.RecordNodeApplications(ctx, sv.ID, first.Revision, first.Hash, "applied", receipt, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.AppliedSubscriptionNode(ctx, nodes[0]); err != nil || ok {
		t.Fatal("stale pruned report reopened fenced resource", err)
	}
}

func TestNewOrdinaryProjectionMutationRestartsExhaustedPublication(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sv, nodes := applicationNodes(t, s)
	applicationPublish(t, s, sv, nodes, "a")
	if _, err := s.db.Exec(`UPDATE server_network_generations SET attempts=?,next_attempt_at='9999-01-01' WHERE server_id=?`, MaxNetworkPublishAttempts, sv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE nodes SET port=port+1 WHERE id=?`, nodes[0].ID); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingNetworkPublications(ctx)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 0 {
		t.Fatal("new ordinary client projection inherited an exhausted retry budget", err)
	}
	if version, err := s.NetworkBindingVersion(ctx, sv.ID); err != nil || version != 0 {
		t.Fatal("ordinary publication required networking opt-in", err)
	}
}
