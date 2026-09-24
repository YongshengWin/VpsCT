package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func egressFixture(t *testing.T, s *Store) (domain.Server, domain.EgressProfile, domain.Node) {
	t.Helper()
	ctx := context.Background()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM servers`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	server := domain.Server{Name: fmt.Sprintf("fixture-%d", count), PublicHost: "server.example.test", Enabled: true}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	profile := domain.EgressProfile{ServerID: server.ID, Name: "fixture outbound", Kind: "direct", Enabled: true}
	if err := s.CreateEgressProfile(ctx, &profile, directFixture("192.0.2.53")); err != nil {
		t.Fatal(err)
	}
	node := domain.Node{Source: domain.NodeDeployed, ServerID: &server.ID, Name: "fixture node", Protocol: "ss", Core: domain.CoreSingBox, Server: server.PublicHost, Port: 21001, ListenPort: 21001, Enabled: true}
	if err := s.CreateNode(ctx, &node); err != nil {
		t.Fatal(err)
	}
	return server, profile, node
}

func directFixture(dns string) json.RawMessage {
	b, _ := json.Marshal(networkconfig.Direct{InterfaceID: strings.Repeat("1", 32), Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: dns, Port: 53}})
	return b
}

func nodePolicy(profile domain.EgressProfile) *networkconfig.Node {
	return &networkconfig.Node{ListenMode: "all", AdvertiseMode: "override", OnUnavailable: "block", EgressProfileID: profile.ID, EgressRevision: profile.CurrentRevision}
}

func TestEgressImmutableRevisionOwnerAndReferenceDeletion(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, profile, n := egressFixture(t, s)
	_, foreign, _ := egressFixture(t, s)
	if _, err := s.SetNodeNetwork(ctx, n.ID, 0, nodePolicy(foreign), nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-server profile accepted", err)
	}
	next, err := s.SetNodeNetwork(ctx, n.ID, 0, nodePolicy(profile), nil)
	if err != nil || next.NetworkRevision != 1 {
		t.Fatal(next, err)
	}
	if _, err := s.SetNodeNetwork(ctx, n.ID, 0, nodePolicy(profile), nil); !errors.Is(err, ErrNetworkConflict) {
		t.Fatal("stale node revision accepted", err)
	}
	profile, err = s.AppendEgressRevision(ctx, profile.ID, 1, "revision two", true, directFixture("192.0.2.54"))
	if err != nil || profile.CurrentRevision != 2 {
		t.Fatal(profile, err)
	}
	if _, err = s.AppendEgressRevision(ctx, profile.ID, 1, "stale", true, directFixture("192.0.2.55")); !errors.Is(err, ErrNetworkConflict) {
		t.Fatal("stale profile revision accepted", err)
	}
	old, err := s.GetEgressRevision(ctx, server.ID, profile.ID, 1)
	if err != nil || string(old.Config) != string(directFixture("192.0.2.53")) {
		t.Fatal("old revision mutated", old, err)
	}
	bound, err := s.GetNode(ctx, n.ID)
	if err != nil || bound.Network.EgressRevision != 1 {
		t.Fatal("profile edit moved a binding", err)
	}
	if _, err = s.db.Exec(`UPDATE egress_profile_revisions SET config='{}' WHERE profile_id=? AND revision=1`, profile.ID); err == nil {
		t.Fatal("immutable revision updated via SQL")
	}
	if _, err = s.db.Exec(`UPDATE node_networks SET server_id=? WHERE node_id=?`, foreign.ServerID, n.ID); err == nil {
		t.Fatal("network owner FK missing")
	}
	if err = s.DeleteEgressProfile(ctx, profile.ID, 2); !errors.Is(err, ErrEgressInUse) {
		t.Fatal("referenced profile deleted", err)
	}
	if _, err = s.db.Exec(`DELETE FROM egress_profile_revisions WHERE profile_id=? AND revision=1`, profile.ID); err == nil {
		t.Fatal("referenced revision deleted via SQL")
	}
	cleared, err := s.SetNodeNetwork(ctx, n.ID, 1, nil, nil)
	if err != nil || cleared.Network != nil || cleared.NetworkRevision != 2 {
		t.Fatal("reset lost CAS history", cleared, err)
	}
	if err = s.DeleteEgressProfile(ctx, profile.ID, 2); err != nil {
		t.Fatal(err)
	}
}

func TestNodeNetworkSurvivesStaleOrdinaryEditsAndCredentialUpdates(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, profile, stale := egressFixture(t, s)
	host := "override.example.test"
	bound, err := s.SetNodeNetwork(ctx, stale.ID, 0, nodePolicy(profile), &host)
	if err != nil {
		t.Fatal(err)
	}
	stale.Name, stale.Server = "renamed", "stale.example.test"
	if err = s.UpdateNode(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	stale.Params, stale.ServerParams = []byte(`{"password":"rotated-fixture"}`), []byte(`{"password":"rotated-fixture"}`)
	if err = s.UpdateNodeCredentials(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateInheritedNodeHosts(ctx, server.ID, "inherited.example.test"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(ctx, stale.ID)
	if err != nil || got.Network == nil || got.NetworkRevision != 1 || got.Server != host || got.Name != "renamed" || string(got.Params) != string(stale.Params) {
		t.Fatal("ordinary operation overwrote network state", got, err)
	}
	policy := *bound.Network
	policy.AdvertiseMode = "inherit"
	got, err = s.SetNodeNetwork(ctx, stale.ID, 1, &policy, nil)
	if err != nil || got.Server != server.PublicHost {
		t.Fatal("inherit did not refresh access address", got, err)
	}
	if err = s.UpdateInheritedNodeHosts(ctx, server.ID, "new.example.test"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetNode(ctx, stale.ID)
	if got.Server != "new.example.test" {
		t.Fatal("inherited node did not follow host")
	}
}

func TestNodeNetworkCASAndServerCleanup(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, profile, node := egressFixture(t, s)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.SetNodeNetwork(ctx, node.ID, 0, nodePolicy(profile), nil)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for err := range errs {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrNetworkConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal("CAS lost a competing edit", ok, conflict)
	}
	if err := s.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal("server deletion failed with network references", err)
	}
	if _, err := s.GetNode(ctx, node.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("server deletion left a node", err)
	}
	var remaining int
	if err := s.db.QueryRow(`SELECT count(*) FROM node_networks WHERE server_id=?`, server.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("network references leaked", err)
	}
	profiles, err := s.ListEgressProfiles(ctx, server.ID)
	if err != nil || len(profiles) != 0 {
		t.Fatal("server profiles leaked", err)
	}
}

func TestCreateBoundNodeIsAtomicAndLegacyNodesRemainUnconfigured(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server, profile, node := egressFixture(t, s)
	legacy, err := s.GetNode(ctx, node.ID)
	if err != nil || legacy.Network != nil || legacy.NetworkRevision != 0 {
		t.Fatal("legacy behavior changed", legacy, err)
	}
	created := legacy
	created.ListenPort, created.Port, created.Network = 21002, 21002, nodePolicy(profile)
	if err := s.CreateNode(ctx, &created); err != nil || created.NetworkRevision != 1 {
		t.Fatal("bound creation failed", err)
	}
	bad := legacy
	bad.ListenPort, bad.Port, bad.Network = 21003, 21003, nodePolicy(profile)
	bad.Network.EgressRevision = 999
	if err := s.CreateNode(ctx, &bad); err == nil {
		t.Fatal("unresolved revision accepted")
	}
	list, err := s.ListNodes(ctx, NodeFilter{ServerID: &server.ID})
	if err != nil || len(list) != 2 {
		t.Fatal("failed bound creation left a node behind", len(list), err)
	}
	manual := domain.Node{Name: "manual", Source: domain.NodeManual, Network: nodePolicy(profile)}
	if s.CreateNode(ctx, &manual) == nil {
		t.Fatal("manual node accepted server network policy")
	}
}
