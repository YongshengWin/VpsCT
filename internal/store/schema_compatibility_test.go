package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestOlderProgramRejectsNewerDatabaseWithoutChangingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	future := len(migrations) + 1
	if _, err = s.db.Exec(`UPDATE schema_version SET version=?`, future); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TABLE future_fixture(value TEXT); INSERT INTO future_fixture VALUES('preserve')`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); !errors.Is(err, ErrSchemaTooNew) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("expected future schema refusal, got %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var value string
	if err = db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil || version != future {
		t.Fatalf("schema changed: %d %v", version, err)
	}
	if err = db.QueryRow(`SELECT value FROM future_fixture`).Scan(&value); err != nil || value != "preserve" {
		t.Fatalf("data changed: %q %v", value, err)
	}
}
