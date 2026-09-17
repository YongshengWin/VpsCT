package store

import (
	"context"
	"ctlvps/internal/domain"
	"testing"
	"time"
)

func TestAuthReadWhileWriterHoldsTransaction(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := &domain.User{Username: "admin", PasswordHash: "fixture", Role: domain.RoleAdmin, Enabled: true}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, Session{ID: "fixture-session", UserID: u.ID, CreatedAt: s.Now(), ExpiresAt: s.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE users SET nickname='pending' WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	// The writer holds the original connection, forcing authentication to open another.
	if _, err = s.GetSession(ctx, "fixture-session"); err != nil {
		t.Fatalf("session read during write: %v", err)
	}
	if _, err = s.GetUserByName(ctx, "admin"); err != nil {
		t.Fatalf("user read during write: %v", err)
	}
}
