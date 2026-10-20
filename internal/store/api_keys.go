package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ctlvps/internal/domain"
)

// MaxActiveAPIKeys bounds the number of usable developer credentials per user.
const MaxActiveAPIKeys = 32

var (
	ErrAPIKeyLimit = errors.New("active API key limit reached")
	ErrConflict    = errors.New("account security state changed")
)

const apiKeySchema = `
CREATE TABLE api_keys (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 token_hash TEXT NOT NULL UNIQUE CHECK(length(token_hash)=64),
 token_hint TEXT NOT NULL DEFAULT '',
 scopes TEXT NOT NULL DEFAULT '[]',
 security_version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 expires_at TEXT,
 last_used_at TEXT,
 revoked_at TEXT
);
CREATE INDEX idx_api_keys_user ON api_keys(user_id,id DESC);
CREATE TRIGGER revoke_api_keys_security AFTER UPDATE OF security_version ON users
WHEN NEW.security_version IS NOT OLD.security_version BEGIN
 UPDATE api_keys SET revoked_at=NEW.updated_at
 WHERE user_id=NEW.id AND revoked_at IS NULL AND security_version<>NEW.security_version;
END;
`

const apiKeyCols = `id,user_id,name,token_hash,token_hint,scopes,security_version,created_at,expires_at,last_used_at,revoked_at`

// Fixed-width UTC timestamps make expiry comparisons exact even at a second
// boundary, where variable-width RFC3339Nano strings do not sort by time.
const apiKeyTimeLayout = "2006-01-02T15:04:05.000000000Z"

func fmtAPIKeyTime(t time.Time) string { return t.UTC().Format(apiKeyTimeLayout) }

func apiKeyTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtAPIKeyTime(*t)
}

func scanAPIKey(sc interface{ Scan(...any) error }) (domain.APIKey, error) {
	var key domain.APIKey
	var scopes, created string
	var expires, used, revoked sql.NullString
	if err := sc.Scan(&key.ID, &key.UserID, &key.Name, &key.TokenHash, &key.TokenHint, &scopes, &key.SecurityVersion, &created, &expires, &used, &revoked); err != nil {
		return key, err
	}
	key.Scopes = jsonList[string](scopes)
	key.CreatedAt = parseTime(created)
	key.ExpiresAt = parseTimePtr(expires)
	key.LastUsedAt = parseTimePtr(used)
	key.RevokedAt = parseTimePtr(revoked)
	return key, nil
}

// CreateAPIKey creates a credential only while the account security version
// observed during authorization still matches. The usable-key limit and insert
// share one transaction, so concurrent requests cannot exceed the limit.
func (s *Store) CreateAPIKey(ctx context.Context, key *domain.APIKey) error {
	now := s.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	scopes := key.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO api_keys(user_id,name,token_hash,token_hint,scopes,security_version,created_at,expires_at)
 SELECT id,?,?,?,?,security_version,?,? FROM users
 WHERE id=? AND enabled=1 AND security_version=?
 AND (SELECT COUNT(*) FROM api_keys WHERE user_id=users.id AND revoked_at IS NULL
      AND security_version=users.security_version AND (expires_at IS NULL OR expires_at>?))<?`,
		key.Name, key.TokenHash, key.TokenHint, jsonStr(scopes), fmtAPIKeyTime(now), apiKeyTimePtr(key.ExpiresAt), key.UserID, key.SecurityVersion, fmtAPIKeyTime(now), MaxActiveAPIKeys)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		var version int64
		var enabled int
		if err := tx.QueryRowContext(ctx, `SELECT security_version,enabled FROM users WHERE id=?`, key.UserID).Scan(&version, &enabled); isNoRows(err) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if enabled != 1 {
			return ErrNotFound
		}
		if version != key.SecurityVersion {
			return ErrConflict
		}
		return ErrAPIKeyLimit
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	key.ID, key.CreatedAt, key.Scopes = id, now, scopes
	key.LastUsedAt, key.RevokedAt = nil, nil
	return nil
}

// ListAPIKeys returns all of one user's credential metadata, including expired
// and revoked keys, newest first. JSON serialization omits credential digests.
func (s *Store) ListAPIKeys(ctx context.Context, userID int64) ([]domain.APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE user_id=? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.APIKey{}
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// GetAPIKeyByHash returns only an enabled account's unexpired, unrevoked key.
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (domain.APIKey, error) {
	key, err := scanAPIKey(s.db.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys
 WHERE token_hash=? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?)
 AND security_version=(SELECT security_version FROM users WHERE users.id=api_keys.user_id AND enabled=1)`, hash, fmtAPIKeyTime(s.Now())))
	if isNoRows(err) {
		return key, ErrNotFound
	}
	return key, err
}

// RevokeAPIKey revokes only a key owned by userID. Repeated revocation is
// idempotent and preserves the first revocation time.
func (s *Store) RevokeAPIKey(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,?) WHERE user_id=? AND id=?`, fmtAPIKeyTime(s.Now()), userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrNotFound
	}
	return err
}

// TouchAPIKey records use while the key and account are still authorized.
func (s *Store) TouchAPIKey(ctx context.Context, id int64) error {
	now := fmtAPIKeyTime(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=? AND revoked_at IS NULL
 AND (expires_at IS NULL OR expires_at>?)
 AND security_version=(SELECT security_version FROM users WHERE users.id=api_keys.user_id AND enabled=1)`, now, id, now)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrNotFound
	}
	return err
}

// DeleteUserAPIKeys permanently deletes all developer credentials for one user.
func (s *Store) DeleteUserAPIKeys(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE user_id=?`, userID)
	return err
}
