package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestTransitRequirementMigrationKeepsCleanupContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v18.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, migration := range migrations[:18] {
		if _, err = db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`
 INSERT INTO schema_version(version) VALUES(18);
 INSERT INTO servers(id,name,created_at,updated_at) VALUES
 (1,'transit','2026-09-20T00:00:00Z','2026-09-20T00:00:00Z'),
 (2,'legacy','2026-09-20T00:00:00Z','2026-09-20T00:00:00Z');
 INSERT INTO nodes(id,name,protocol,source,server_id,created_at,updated_at) VALUES
 (1,'fixture','ss','deployed',1,'2026-09-20T00:00:00Z','2026-09-20T00:00:00Z');
 INSERT INTO egress_profiles(id,server_id,name,kind,enabled,current_revision,created_at,updated_at) VALUES
 (1,1,'upstream','socks5',1,1,'2026-09-20T00:00:00Z','2026-09-20T00:00:00Z');
 INSERT INTO egress_profile_revisions(profile_id,revision,server_id,config,created_at) VALUES
 (1,1,1,'{}','2026-09-20T00:00:00Z');
 INSERT INTO node_networks(node_id,server_id,revision,policy,egress_profile_id,egress_revision,updated_at) VALUES
 (1,1,1,'{"listen_mode":"all","advertise_mode":"inherit","on_unavailable":"block","egress_profile_id":1,"egress_revision":1}',1,1,'2026-09-20T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for id, want := range map[int64]int{1: 1, 2: 0} {
		if got, err := s.NetworkEgressVersion(ctx, id); err != nil || got != want {
			t.Fatal("migration lost or invented transit requirement", id, got, err)
		}
	}
	if _, err = s.SetNodeNetwork(ctx, 1, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteNode(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteEgressProfile(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := s.NetworkEgressVersion(ctx, 1); err != nil || got != 1 {
		t.Fatal("detachment/deletion erased transit requirement", err)
	}
	if err = s.DeleteServer(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := s.NetworkEgressVersion(ctx, 1); err != nil || got != 0 {
		t.Fatal("server deletion left orphan requirement", err)
	}
}
