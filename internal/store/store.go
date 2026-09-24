// Package store is the SQLite persistence layer of ctlvpsd.
package store

import (
	"context"
	"crypto/cipher"
	"ctlvps/internal/backup"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the main SQLite database.
type Store struct {
	secret          cipher.AEAD
	migratedSecrets bool
	db              *sql.DB
	Now             func() time.Time
}

// Open opens (creating if needed) the database at path and applies migrations.
// Use ":memory:" for tests.
func Open(path string) (*Store, error) {
	keyPath := path + ".key"
	if path == ":memory:" {
		keyPath = ""
	}
	return OpenWithKey(path, keyPath)
}

func OpenWithKey(path, keyPath string) (*Store, error) {
	secret, err := loadSecretKey(keyPath)
	if err != nil {
		return nil, err
	}
	dsn := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		dsn = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=secure_delete(1)&_pragma=max_page_count(262144)&_pragma=journal_size_limit(16777216)"
	} else {
		dsn = "file::memory:?cache=shared&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(8)
	}
	s := &Store{secret: secret, db: db, Now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateSecrets(); err != nil {
		db.Close()
		return nil, err
	}
	if s.migratedSecrets && path != ":memory:" {
		if _, err = s.db.Exec("VACUUM; PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if path != ":memory:" {
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Chmod(p, 0600); err != nil && !os.IsNotExist(err) {
				db.Close()
				return nil, err
			}
		}
	}
	if err := s.SplitInlineChains(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying handle for advanced callers (tests, backups).
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	ctx := context.Background()
	var version int
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_version`)
	if err := row.Scan(&version); err != nil {
		// table missing => fresh database
		if !strings.Contains(err.Error(), "no such table") {
			return err
		}
		version = 0
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if migrations[i] == coreVersionPinMigration {
			if err := s.pinInitialCoreVersion(ctx, tx, version == 0); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Backup writes a consistent snapshot of the database to dst using VACUUM INTO.
func (s *Store) Backup(ctx context.Context, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(filepath.Dir(dst), ".snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	snapshot := filepath.Join(dir, "data.db")
	if _, err = s.db.ExecContext(ctx, `VACUUM INTO ?`, snapshot); err != nil {
		return err
	}
	if err = os.Chmod(snapshot, 0600); err != nil {
		return err
	}
	return backup.File(snapshot, dst, s.secret, false)
}

// ---- helpers ----

const tsLayout = time.RFC3339Nano

func fmtTime(t time.Time) string { return t.UTC().Format(tsLayout) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(tsLayout, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}
		}
	}
	return t
}

func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTime(ns.String)
	return &t
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func intPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func jsonStr(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func jsonList[T any](s string) []T {
	var out []T
	if s == "" {
		return []T{}
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []T{}
	}
	return out
}

func rawOrEmpty(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// Tx runs fn inside a transaction.
func (s *Store) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// querier abstracts *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
