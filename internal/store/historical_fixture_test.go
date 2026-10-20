package store

import (
	"ctlvps/internal/domain"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// Build the actual historical schema prefix; rewinding only its marker on a
// current database accidentally retains later triggers, tables and indexes.
func historicalStore(t *testing.T, version int) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "historical.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:version] {
		if _, err := db.Exec(migration); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO schema_version(version) VALUES(?)`, version); err != nil {
		t.Fatal(err)
	}
	secret, err := loadSecretKey(path + ".key")
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, secret: secret, Now: func() time.Time { return time.Now().UTC() }}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// Insert released server columns directly: modern CreateServer requires the current schema.
func historicalCreateServer(t *testing.T, s *Store, v *domain.Server) {
	t.Helper()
	now := fmtTime(s.Now())
	res, err := s.db.Exec(`INSERT INTO servers(name,public_host,quota_billing,core_mode,cert_mode,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, v.Name, v.PublicHost, v.QuotaBilling, v.CoreMode, v.CertMode, b2i(v.Enabled), now, now)
	if err != nil {
		t.Fatal(err)
	}
	v.ID, _ = res.LastInsertId()
	if _, err = s.db.Exec(`INSERT INTO agents(server_id,created_at,updated_at) VALUES(?,?,?)`, v.ID, now, now); err != nil {
		t.Fatal(err)
	}
}
