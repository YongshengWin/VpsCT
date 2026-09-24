package store

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Exercise the complete released v0.1.4 schema, including encrypted values,
// rather than reconstructing only the immediately preceding migration.
func TestRelease015UpgradeFromSchema11PreservesData(t *testing.T) {
	for _, pin := range []string{"", "1.13.21"} {
		t.Run("pin="+pin, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "controller.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, migration := range migrations[:11] {
				if _, err := db.Exec(migration); err != nil {
					t.Fatal(err)
				}
			}
			secret, err := loadSecretKey(path + ".key")
			if err != nil {
				t.Fatal(err)
			}
			old := &Store{db: db, secret: secret}
			stamp := "2026-09-17T00:00:00Z"
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO schema_version VALUES(11)`)
			exec(`INSERT INTO users(id,username,password_hash,role,created_at,updated_at) VALUES(1,'fixture-admin','fixture-hash','admin',?,?)`, stamp, stamp)
			exec(`INSERT INTO servers(id,name,public_host,quota_bytes,created_at,updated_at) VALUES(1,'fixture','server.example.test',1000000,?,?)`, stamp, stamp)
			exec(`INSERT INTO shares(id,name,user_id,targets,period_start,used_upload,used_download,created_at,updated_at) VALUES(1,'fixture-share',1,'[{"server_id":1,"protocols":["ss"]}]',?,123,456,?,?)`, stamp, stamp, stamp)
			exec(`INSERT INTO nodes(id,name,protocol,server,port,params,server_params,source,server_id,listen_port,core,share_id,created_at,updated_at) VALUES(1,'fixture-node','ss','node.example.test',21000,?,?,'deployed',1,21000,'singbox',1,?,?)`, old.seal("nodes.params", `{"password":"fixture-only"}`), old.seal("nodes.server_params", `{"password":"fixture-only"}`), stamp, stamp)
			exec(`INSERT INTO subscriptions(id,name,kind,token,token_hash,share_id,created_at,updated_at) VALUES(1,'fixture-sub','nodes',?,'fixture-hash',1,?,?)`, old.seal("subscriptions.token", "fixture-token"), stamp, stamp)
			exec(`INSERT INTO traffic_samples(server_id,node_id,ts,rx_bytes,tx_bytes) VALUES(1,1,?,123,456)`, stamp)
			if pin != "" {
				exec(`INSERT INTO settings(key,value) VALUES('core.singbox_version',?)`, old.seal("settings.value", pin))
			}
			queries := []string{
				`SELECT id,username,password_hash,role,enabled FROM users ORDER BY id`,
				`SELECT id,name,public_host,quota_bytes FROM servers ORDER BY id`,
				`SELECT id,params,server_params,server_id,share_id,listen_port FROM nodes ORDER BY id`,
				`SELECT id,targets,used_upload,used_download,status FROM shares ORDER BY id`,
				`SELECT id,token,token_hash,share_id,enabled FROM subscriptions ORDER BY id`,
				`SELECT server_id,node_id,ts,rx_bytes,tx_bytes FROM traffic_samples ORDER BY ts`,
			}
			snapshot := func(db *sql.DB) [][][]any {
				t.Helper()
				var out [][][]any
				for _, q := range queries {
					rows, err := db.Query(q)
					if err != nil {
						t.Fatal(err)
					}
					columns, err := rows.Columns()
					if err != nil {
						t.Fatal(err)
					}
					var records [][]any
					for rows.Next() {
						values := make([]any, len(columns))
						dest := make([]any, len(columns))
						for i := range values {
							dest[i] = &values[i]
						}
						if err := rows.Scan(dest...); err != nil {
							t.Fatal(err)
						}
						records = append(records, values)
					}
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
					rows.Close()
					out = append(out, records)
				}
				return out
			}
			before := snapshot(db)
			key, err := os.ReadFile(path + ".key")
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				st, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, snapshot(st.db)) {
					st.Close()
					t.Fatal("released data changed during migration or reopen")
				}
				var version int
				if err := st.db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil || version != len(migrations) {
					t.Fatal("incorrect schema", version, err)
				}
				want := pin
				if want == "" {
					want = "1.12.14"
				}
				if got := st.GetSetting(context.Background(), "core.singbox_version", ""); got != want {
					t.Fatal("version selection changed")
				}
				n, err := st.GetNode(context.Background(), 1)
				if err != nil || string(n.Params) != `{"password":"fixture-only"}` || n.Network != nil {
					t.Fatal("credential decryption or legacy network changed", err)
				}
				var encrypted string
				if err := st.db.QueryRow(`SELECT token FROM subscriptions WHERE id=1`).Scan(&encrypted); err != nil {
					t.Fatal(err)
				}
				if value, err := st.decrypt("subscriptions.token", encrypted); err != nil || value != "fixture-token" {
					t.Fatal("subscription credential no longer decrypts", err)
				}
				rows, err := st.db.Query(`PRAGMA foreign_key_check`)
				if err != nil {
					t.Fatal(err)
				}
				if rows.Next() || rows.Err() != nil {
					t.Fatal("foreign key violation")
				}
				rows.Close()
				st.Close()
			}
			afterKey, err := os.ReadFile(path + ".key")
			if err != nil || !bytes.Equal(key, afterKey) {
				t.Fatal("encryption key changed", err)
			}
		})
	}
}
