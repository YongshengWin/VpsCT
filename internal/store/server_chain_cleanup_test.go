package store

import (
	"context"
	"path/filepath"
	"testing"

	"ctlvps/internal/domain"
)

func TestDeleteServerChainUsesLandingIdentityAfterRotation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	server := domain.Server{Name: "removed"}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	landing := chainOriginNode(t, s, "landing")
	landing.ServerID = &server.ID
	if err := s.UpdateNode(ctx, &landing); err != nil {
		t.Fatal(err)
	}
	front := chainOriginNode(t, s, "front")
	chain := landing
	chain.ID, chain.Source, chain.ServerID, chain.ChainFrontNodeID = 0, domain.NodeChain, nil, &front.ID
	if err := s.CreateChainNode(ctx, &chain, landing.ID); err != nil {
		t.Fatal(err)
	}
	// An independent chain with an explicit origin must survive even if its
	// copied endpoint happens to match the server being removed.
	other := chainOriginNode(t, s, "other")
	safe := chain
	safe.ID = 0
	if err := s.CreateChainNode(ctx, &safe, other.ID); err != nil {
		t.Fatal(err)
	}
	landing.Server, landing.Port = "rotated.example.test", 22002
	if err := s.UpdateNode(ctx, &landing); err != nil {
		t.Fatal(err)
	}
	safe.Server, safe.Port = landing.Server, landing.Port
	if err := s.UpdateNode(ctx, &safe); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNode(ctx, chain.ID); err != ErrNotFound {
		t.Fatalf("chain with rotated landing survived: %v", err)
	}
	for _, id := range []int64{front.ID, other.ID, safe.ID} {
		if _, err := s.GetNode(ctx, id); err != nil {
			t.Fatalf("independent node %d removed: %v", id, err)
		}
	}
}

func TestOpenPrunesHistoricalChainsWithMissingFront(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chains.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	front := chainOriginNode(t, s, "front")
	landing := chainOriginNode(t, s, "landing")
	chain := landing
	chain.ID, chain.Source, chain.ChainFrontNodeID = 0, domain.NodeChain, &front.ID
	if err := s.CreateChainNode(ctx, &chain, landing.ID); err != nil {
		t.Fatal(err)
	}
	dependent := chain
	dependent.ID, dependent.ChainFrontNodeID = 0, &chain.ID
	if err := s.CreateChainNode(ctx, &dependent, landing.ID); err != nil {
		t.Fatal(err)
	}
	safe := chain
	safe.ID, safe.ChainFrontNodeID = 0, &landing.ID
	if err := s.CreateChainNode(ctx, &safe, landing.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate an older controller deleting the original node without chains.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, front.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{chain.ID, dependent.ID} {
		if _, err := s.GetNode(ctx, id); err != ErrNotFound {
			t.Fatalf("orphan chain %d survived restart: %v", id, err)
		}
	}
	for _, id := range []int64{landing.ID, safe.ID} {
		if _, err := s.GetNode(ctx, id); err != nil {
			t.Fatalf("independent node %d removed: %v", id, err)
		}
	}
	if err := s.PruneOrphanChains(ctx); err != nil {
		t.Fatal("repeated cleanup", err)
	}
}
