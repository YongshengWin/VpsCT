package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"ctlvps/internal/domain"
)

func chainOriginNode(t *testing.T, s *Store, password string) domain.Node {
	t.Helper()
	params, err := json.Marshal(map[string]string{"cipher": "aes-128-gcm", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	n := domain.Node{Name: "chain origin fixture", Protocol: "ss", Server: "chain.example.test", Port: 21001, Params: params, Source: domain.NodeManual, Enabled: true}
	if err := s.CreateNode(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChainOriginExplicitIdentitySurvivesRotationAndAmbiguousEndpoint(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	chainOriginNode(t, s, "same-fixture")
	landing := chainOriginNode(t, s, "same-fixture")
	chain := landing
	chain.ID, chain.Source = 0, domain.NodeChain
	if err := s.CreateChainNode(ctx, &chain, landing.ID); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.ChainLanding(ctx, chain.ID)
	if err != nil || !ok || got.ID != landing.ID {
		t.Fatal("explicit identity was replaced by an inferred endpoint", err)
	}
	landing.Params = json.RawMessage(`{"cipher":"aes-128-gcm","password":"rotated-fixture"}`)
	landing.Server, landing.Port = "rotated.example.test", 21002
	if err := s.UpdateNode(ctx, &landing); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.ChainLanding(ctx, chain.ID)
	if err != nil || !ok || got.ID != landing.ID || got.Server != landing.Server || got.Port != landing.Port || !equalApplicationJSON(got.Params, landing.Params) {
		t.Fatal("legitimate rotation lost the explicitly selected identity", err)
	}
}

func TestChainOriginDeletionAndAmbiguityRemainFencedAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chain.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	first := chainOriginNode(t, s, "same-fixture")
	second := chainOriginNode(t, s, "same-fixture")
	explicit := first
	explicit.ID, explicit.Source = 0, domain.NodeChain
	if err := s.CreateChainNode(ctx, &explicit, first.ID); err != nil {
		t.Fatal(err)
	}
	ambiguous := explicit
	ambiguous.ID = 0
	if err := s.CreateNode(ctx, &ambiguous); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ChainLanding(ctx, ambiguous.ID); err != nil || ok {
		t.Fatal("ambiguous origin was selected", err)
	}
	for _, id := range []int64{first.ID, second.ID} {
		if err := s.DeleteNode(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	chainOriginNode(t, s, "same-fixture") // one new exact endpoint must not reopen either chain
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{explicit.ID, ambiguous.ID} {
		if _, ok, err := s.ChainLanding(ctx, id); err != nil || ok {
			t.Fatal("restart rebound a deleted or ambiguous origin to a replacement", err)
		}
		var landing sql.NullInt64
		var resolved bool
		if err := s.db.QueryRowContext(ctx, `SELECT landing_id,resolved FROM node_chain_origins WHERE chain_id=?`, id).Scan(&landing, &resolved); err != nil || landing.Valid || !resolved {
			t.Fatal("resolved null origin lost its permanent fence", err)
		}
	}
}

func TestHistoricalChainOriginMigrationUsesExactDecryptedCredential(t *testing.T) {
	s, path := historicalStore(t, 32)
	ctx := context.Background()
	chainOriginNode(t, s, "wrong-fixture") // lower ID must not steal the origin
	landing := chainOriginNode(t, s, "right-fixture")
	chain := landing
	chain.ID, chain.Source = 0, domain.NodeChain
	if err := s.CreateNode(ctx, &chain); err != nil {
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
	got, ok, err := current.ChainLanding(ctx, chain.ID)
	if err != nil || !ok || got.ID != landing.ID {
		t.Fatal("historical migration chose the wrong credential at the same endpoint", err)
	}
	landing.Params = json.RawMessage(`{"cipher":"aes-128-gcm","password":"rotated-fixture"}`)
	if err := current.UpdateNode(ctx, &landing); err != nil {
		t.Fatal(err)
	}
	got, ok, err = current.ChainLanding(ctx, chain.ID)
	if err != nil || !ok || got.ID != landing.ID || !equalApplicationJSON(got.Params, landing.Params) {
		t.Fatal("migrated origin was re-inferred after a legitimate rotation", err)
	}
}
