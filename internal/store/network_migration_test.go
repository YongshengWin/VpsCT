package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func TestNetworkBindingMigrationPreservesLegacyNodesAndShares(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v13.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const beforeBinding = 13
	for _, migration := range migrations[:beforeBinding] {
		if _, err = db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO schema_version(version) VALUES (?)`, []any{beforeBinding}},
		{`INSERT INTO servers(id,name,public_host,created_at,updated_at) VALUES (1,'fixture','server.example.test',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO shares(id,name,targets,period_start,created_at,updated_at) VALUES (1,'legacy share','[{"server_id":1,"protocols":["ss"]}]',?,?,?)`, []any{stamp, stamp, stamp}},
		{`INSERT INTO nodes(id,name,protocol,server,port,params,server_params,source,server_id,listen_port,core,share_id,created_at,updated_at) VALUES (1,'deployed','ss','node.example.test',21000,'{"password":"fixture"}','{"password":"fixture"}','deployed',1,21000,'singbox',1,?,?)`, []any{stamp, stamp}},
		{`INSERT INTO nodes(id,name,protocol,server,port,source,created_at,updated_at) VALUES (2,'manual','ss','manual.example.test',21001,'manual',?,?)`, []any{stamp, stamp}},
	}
	for _, statement := range statements {
		if _, err = db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	nodes, err := st.ListNodes(ctx, NodeFilter{})
	if err != nil || len(nodes) != 2 {
		t.Fatal("migration changed node count", err)
	}
	for _, node := range nodes {
		if node.Network != nil || node.NetworkRevision != 0 {
			t.Fatal("migration opted a legacy node into networking")
		}
	}
	node, err := st.GetNode(ctx, 1)
	if err != nil || node.Server != "node.example.test" || node.ListenPort != 21000 || node.Source != domain.NodeDeployed || node.ShareID == nil || *node.ShareID != 1 || string(node.Params) != `{"password":"fixture"}` || string(node.ServerParams) != `{"password":"fixture"}` {
		t.Fatal("legacy endpoint, credentials or ownership changed", err)
	}
	share, err := st.GetShare(ctx, 1)
	if err != nil || len(share.Targets) != 1 || share.Targets[0].ServerID != 1 {
		t.Fatal("legacy deployment target changed", err)
	}
	profiles, err := st.ListEgressProfiles(ctx, 1)
	if err != nil || len(profiles) != 0 {
		t.Fatal("migration synthesized egress profiles", err)
	}
	rows, err := st.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("foreign key violation after upgrade")
	}
}

func TestNetworkRequirementMigrationBackfillsAndSurvivesDetach(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v14.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const beforeRequirement = 14
	for _, migration := range migrations[:beforeRequirement] {
		if _, err = db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
 INSERT INTO schema_version(version) VALUES(14);
 INSERT INTO servers(id,name,created_at,updated_at) VALUES(1,'bound','2026-09-20T00:00:00Z','2026-09-20T00:00:00Z'),(2,'legacy','2026-09-20T00:00:00Z','2026-09-20T00:00:00Z');
 INSERT INTO nodes(id,name,protocol,source,server_id,created_at,updated_at) VALUES(1,'fixture','ss','deployed',1,'2026-09-20T00:00:00Z','2026-09-20T00:00:00Z');
 INSERT INTO node_networks(node_id,server_id,revision,policy,updated_at) VALUES(1,1,1,'{"listen_mode":"all","advertise_mode":"inherit","on_unavailable":"block"}','2026-09-20T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for id, want := range map[int64]int{1: 1, 2: 0} {
		if got, err := st.NetworkBindingVersion(ctx, id); err != nil || got != want {
			t.Fatal("migration lost or invented network requirement", got, err)
		}
	}
	if _, err := st.SetNodeNetwork(ctx, 1, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNode(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := st.NetworkBindingVersion(ctx, 1); err != nil || got != 1 {
		t.Fatal("detachment incorrectly allowed an agent downgrade", err)
	}
	if err := st.DeleteServer(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := st.NetworkBindingVersion(ctx, 1); err != nil || got != 0 {
		t.Fatal("server deletion left an orphan requirement", err)
	}
}
