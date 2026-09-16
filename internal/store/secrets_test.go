package store

import (
	"bytes"
	"context"
	"ctlvps/internal/domain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSecretMigrationEncryptedBackupAndRecovery(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "data.db")
	key := filepath.Join(dir, "key")
	ctx := context.Background()
	s, e := OpenWithKey(db, key)
	if e != nil {
		t.Fatal(e)
	}
	user := domain.User{Username: "fixture", PasswordHash: "not-a-real-password-hash", Enabled: true, Role: domain.RoleAdmin}
	if e = s.CreateUser(ctx, &user); e != nil {
		t.Fatal(e)
	}
	if e = s.CreateSession(ctx, Session{ID: "fixture-session", UserID: user.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	// Simulate a legacy plaintext row, then reopen through the migration.
	if _, e = s.db.Exec("INSERT INTO settings(key,value) VALUES('fixture.secret','synthetic-private-value')"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = OpenWithKey(db, key)
	if e != nil {
		t.Fatal(e)
	}
	if got := s.GetSetting(ctx, "fixture.secret", ""); got != "synthetic-private-value" {
		t.Fatal("migration changed secret")
	}
	var raw string
	if e = s.db.QueryRow("SELECT value FROM settings WHERE key='fixture.secret'").Scan(&raw); e != nil || !bytes.HasPrefix([]byte(raw), []byte(secretPrefix)) {
		t.Fatal("plaintext persisted", e)
	}
	dst := filepath.Join(dir, "backup.enc")
	if e = s.Backup(ctx, dst); e != nil {
		t.Fatal(e)
	}
	s.Close()
	data, _ := os.ReadFile(dst)
	if bytes.Contains(data, []byte("synthetic-private-value")) || bytes.HasPrefix(data, []byte("SQLite")) {
		t.Fatal("backup is plaintext")
	}
	restored := filepath.Join(dir, "restored.db")
	if _, e = BackupEntry([]string{"backup", "restore", key, dst, restored}); e != nil {
		t.Fatal(e)
	}
	s, e = OpenWithKey(restored, key)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.GetSession(ctx, "fixture-session"); e != ErrNotFound {
		t.Fatal("old session survived restore", e)
	}
	if s.GetSetting(ctx, "fixture.secret", "") != "synthetic-private-value" {
		t.Fatal("restore lost secret")
	}
	wrong := filepath.Join(dir, "wrong")
	_ = os.WriteFile(wrong, make([]byte, 32), 0600)
	if _, e = OpenWithKey(restored, wrong); e == nil {
		t.Fatal("wrong data key accepted")
	}
}
