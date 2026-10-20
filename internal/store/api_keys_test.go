package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func apiKeyTestUser(t *testing.T, s *Store, name string) domain.User {
	t.Helper()
	u := domain.User{Username: name, PasswordHash: "fixture-password-hash", Role: domain.RoleAdmin, Enabled: true}
	if err := s.CreateUser(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func apiKeyFixture(u domain.User, name string) domain.APIKey {
	digest := sha256.Sum256([]byte("fixture-developer-credential-" + name))
	return domain.APIKey{UserID: u.ID, SecurityVersion: u.SecurityVersion, Name: name, TokenHash: hex.EncodeToString(digest[:]), TokenHint: "vpsct_…fixture", Scopes: []string{"read", "write"}}
}

func TestAPIKeyStoresDigestAndMetadata(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := apiKeyTestUser(t, s, "owner")
	key := apiKeyFixture(u, "automation")
	expires := s.Now().Add(time.Hour)
	key.ExpiresAt = &expires
	if err := s.CreateAPIKey(ctx, &key); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAPIKeyByHash(ctx, key.TokenHash)
	if err != nil || got.ID != key.ID || got.UserID != u.ID || got.Name != key.Name || got.TokenHint != key.TokenHint || got.CreatedAt.IsZero() || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "read" || got.Scopes[1] != "write" {
		t.Fatalf("scopes: %v", got.Scopes)
	}
	var digest string
	if err := s.db.QueryRow(`SELECT token_hash FROM api_keys WHERE id=?`, key.ID).Scan(&digest); err != nil || digest != key.TokenHash || strings.Contains(digest, "fixture-developer-credential-") {
		t.Fatalf("stored digest: %q %v", digest, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), key.TokenHash) || strings.Contains(string(encoded), "security_version") || strings.Contains(string(encoded), "fixture-developer-credential-") {
		t.Fatalf("public JSON must exclude secrets: %s %v", encoded, err)
	}
	duplicate := apiKeyFixture(u, "duplicate")
	duplicate.TokenHash = key.TokenHash
	if err := s.CreateAPIKey(ctx, &duplicate); err == nil {
		t.Fatal("duplicate digest was accepted")
	}
}

func TestAPIKeyOwnershipRevocationAndDeletion(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := apiKeyTestUser(t, s, "owner")
	other := apiKeyTestUser(t, s, "other")
	key := apiKeyFixture(u, "owner-key")
	otherKey := apiKeyFixture(other, "other-key")
	for _, k := range []*domain.APIKey{&key, &otherKey} {
		if err := s.CreateAPIKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []domain.User{u, other} {
		keys, err := s.ListAPIKeys(ctx, user.ID)
		if err != nil || len(keys) != 1 || keys[0].UserID != user.ID {
			t.Fatalf("user key isolation: %+v %v", keys, err)
		}
	}
	if err := s.RevokeAPIKey(ctx, other.ID, key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user revoke: %v", err)
	}
	now := s.Now()
	s.Now = func() time.Time { return now }
	if err := s.TouchAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAPIKeyByHash(ctx, key.TokenHash)
	if err != nil || got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Fatalf("last used: %+v %v", got, err)
	}
	if err := s.RevokeAPIKey(ctx, u.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return now.Add(time.Minute) }
	if err := s.RevokeAPIKey(ctx, u.ID, key.ID); err != nil {
		t.Fatalf("repeat revoke: %v", err)
	}
	if _, err := s.GetAPIKeyByHash(ctx, key.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked lookup: %v", err)
	}
	if err := s.TouchAPIKey(ctx, key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked touch: %v", err)
	}
	keys, err := s.ListAPIKeys(ctx, u.ID)
	if err != nil || len(keys) != 1 || keys[0].RevokedAt == nil || !keys[0].RevokedAt.Equal(now) {
		t.Fatalf("revocation metadata: %+v %v", keys, err)
	}
	if err := s.DeleteUserAPIKeys(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if keys, err := s.ListAPIKeys(ctx, u.ID); err != nil || len(keys) != 0 {
		t.Fatalf("delete user keys: %+v %v", keys, err)
	}
	if _, err := s.GetAPIKeyByHash(ctx, otherKey.TokenHash); err != nil {
		t.Fatalf("other user's key was affected: %v", err)
	}
	if err := s.DeleteUser(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_keys`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("user deletion did not cascade: %d %v", count, err)
	}
}

func TestAPIKeyExactExpiry(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := apiKeyTestUser(t, s, "owner")
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	for _, tc := range []struct {
		name   string
		expiry time.Time
		valid  bool
	}{
		{"expired", now.Add(-time.Nanosecond), false},
		{"boundary", now, false},
		{"future", now.Add(time.Nanosecond), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := apiKeyFixture(u, tc.name)
			key.ExpiresAt = &tc.expiry
			if err := s.CreateAPIKey(ctx, &key); err != nil {
				t.Fatal(err)
			}
			_, err := s.GetAPIKeyByHash(ctx, key.TokenHash)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrNotFound) {
				t.Fatalf("expiry lookup: %v", err)
			}
			err = s.TouchAPIKey(ctx, key.ID)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrNotFound) {
				t.Fatalf("expiry touch: %v", err)
			}
		})
	}
}

func TestAPIKeyAccountSecurityChangesRevokeCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domain.User)
	}{
		{"password", func(u *domain.User) { u.PasswordHash = "updated-fixture-hash" }},
		{"role", func(u *domain.User) { u.Role = domain.RoleUser }},
		{"disabled", func(u *domain.User) { u.Enabled = false }},
		{"two-factor", func(u *domain.User) { u.TOTPEnabled = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			u := apiKeyTestUser(t, s, "owner")
			key := apiKeyFixture(u, tc.name)
			if err := s.CreateAPIKey(ctx, &key); err != nil {
				t.Fatal(err)
			}
			tc.change(&u)
			if err := s.UpdateUser(ctx, &u); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetAPIKeyByHash(ctx, key.TokenHash); !errors.Is(err, ErrNotFound) {
				t.Fatalf("credential survived %s change: %v", tc.name, err)
			}
			keys, err := s.ListAPIKeys(ctx, u.ID)
			if err != nil || len(keys) != 1 || keys[0].RevokedAt == nil {
				t.Fatalf("security revocation metadata: %+v %v", keys, err)
			}
			stale := apiKeyFixture(u, "stale-authorization")
			err = s.CreateAPIKey(ctx, &stale)
			want := ErrConflict
			if !u.Enabled {
				want = ErrNotFound
			}
			if !errors.Is(err, want) {
				t.Fatalf("stale authorization creation: got %v want %v", err, want)
			}
			current, err := s.GetUser(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !current.Enabled {
				current.Enabled = true
				if err := s.UpdateUser(ctx, &current); err != nil {
					t.Fatal(err)
				}
				current, err = s.GetUser(ctx, u.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.GetAPIKeyByHash(ctx, key.TokenHash); !errors.Is(err, ErrNotFound) {
					t.Fatalf("old credential revived after re-enable: %v", err)
				}
			}
			fresh := apiKeyFixture(current, "fresh-authorization")
			if err := s.CreateAPIKey(ctx, &fresh); err != nil {
				t.Fatalf("current authorization creation: %v", err)
			}
		})
	}
}

func TestAPIKeyProfileAndSessionChangesPreserveCredentials(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := apiKeyTestUser(t, s, "owner")
	key := apiKeyFixture(u, "automation")
	if err := s.CreateAPIKey(ctx, &key); err != nil {
		t.Fatal(err)
	}
	u.Nickname = "Updated profile"
	if err := s.UpdateUser(ctx, &u); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUserSessions(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAPIKeyByHash(ctx, key.TokenHash); err != nil {
		t.Fatalf("unrelated profile/session change revoked key: %v", err)
	}
}

func TestAPIKeySecurityFactorRotationRevokesCredentials(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := apiKeyTestUser(t, s, "owner")
	u.TOTPEnabled, u.TOTPSecret = true, "fixture-old-factor"
	if err := s.UpdateUser(ctx, &u); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := apiKeyFixture(before, "automation")
	if err := s.CreateAPIKey(ctx, &key); err != nil {
		t.Fatal(err)
	}
	after := before
	after.TOTPSecret = "fixture-new-factor"
	if err := s.UpdateUserSecurity(ctx, before, after); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAPIKeyByHash(ctx, key.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("credential survived factor rotation: %v", err)
	}
	keys, err := s.ListAPIKeys(ctx, u.ID)
	if err != nil || len(keys) != 1 || keys[0].RevokedAt == nil {
		t.Fatalf("factor rotation metadata: %+v %v", keys, err)
	}
}

func TestAPIKeyLimitIsAtomicAndIgnoresInactiveKeys(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u := apiKeyTestUser(t, s, "owner")
	now := s.Now()
	s.Now = func() time.Time { return now }
	expired := apiKeyFixture(u, "expired")
	expiry := now
	expired.ExpiresAt = &expiry
	if err := s.CreateAPIKey(ctx, &expired); err != nil {
		t.Fatal(err)
	}
	revoked := apiKeyFixture(u, "revoked")
	if err := s.CreateAPIKey(ctx, &revoked); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAPIKey(ctx, u.ID, revoked.ID); err != nil {
		t.Fatal(err)
	}
	for i := range MaxActiveAPIKeys - 1 {
		key := apiKeyFixture(u, fmt.Sprintf("existing-%d", i))
		if err := s.CreateAPIKey(ctx, &key); err != nil {
			t.Fatalf("create existing key %d: %v", i, err)
		}
	}
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			key := apiKeyFixture(u, fmt.Sprintf("concurrent-%d", i))
			if err := s.CreateAPIKey(ctx, &key); err == nil {
				created.Add(1)
			} else if !errors.Is(err, ErrAPIKeyLimit) {
				t.Errorf("unexpected concurrent creation error: %v", err)
			}
		})
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("concurrent creation count: %d", created.Load())
	}
	keys, err := s.ListAPIKeys(ctx, u.ID)
	if err != nil || len(keys) != MaxActiveAPIKeys+2 {
		t.Fatalf("key count: %d %v", len(keys), err)
	}
}
