package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"ctlvps/internal/domain"
)

const userCols = `security_version, id, username, nickname, password_hash, role, enabled, avatar, totp_secret, totp_enabled, totp_last_step, recovery_codes, last_login_at, created_at, updated_at`

func (s *Store) scanUser(sc interface{ Scan(...any) error }) (domain.User, error) {
	var u domain.User
	var enabled, totpEnabled int
	var created, updated, recovery string
	var lastLogin sql.NullString
	if err := sc.Scan(&u.SecurityVersion, &u.ID, &u.Username, &u.Nickname, &u.PasswordHash, &u.Role, &enabled, &u.Avatar, s.scanSecret("users.totp_secret", &u.TOTPSecret), &totpEnabled, &u.TOTPLastStep, &recovery, &lastLogin, &created, &updated); err != nil {
		return u, err
	}
	u.Enabled = enabled == 1
	u.TOTPEnabled = totpEnabled == 1
	_ = json.Unmarshal([]byte(recovery), &u.RecoveryCodes)
	if lastLogin.Valid && lastLogin.String != "" {
		t := parseTime(lastLogin.String)
		u.LastLoginAt = &t
	}
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return u, nil
}

func recoveryJSON(codes []string) string {
	if codes == nil {
		codes = []string{}
	}
	b, _ := json.Marshal(codes)
	return string(b)
}

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

var ErrAlreadyInitialized = errors.New("system already initialized")

// CreateInitialAdmin checks and inserts atomically, including concurrent setup
// requests arriving over different database connections.
func (s *Store) CreateInitialAdmin(ctx context.Context, u *domain.User) error {
	now := s.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,enabled,created_at,updated_at)
		SELECT ?,?,'admin',1,?,? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		u.Username, u.PasswordHash, fmtTime(now), fmtTime(now))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAlreadyInitialized
	}
	u.ID, err = res.LastInsertId()
	u.Role, u.Enabled = domain.RoleAdmin, true
	u.CreatedAt, u.UpdatedAt = now, now
	return err
}

// CreateUser inserts a user.
func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	now := s.Now()
	if u.Role == "" {
		u.Role = domain.RoleUser
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO users(username,nickname,password_hash,role,enabled,avatar,recovery_codes,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		u.Username, u.Nickname, u.PasswordHash, u.Role, b2i(u.Enabled), u.Avatar, recoveryJSON(u.RecoveryCodes), fmtTime(now), fmtTime(now))
	if err != nil {
		return err
	}
	u.ID, _ = res.LastInsertId()
	u.CreatedAt, u.UpdatedAt = now, now
	return nil
}

// UpdateUser persists every mutable field (profile, credentials, 2FA state).
func (s *Store) UpdateUser(ctx context.Context, u *domain.User) error {
	now := s.Now()
	res, err := s.db.ExecContext(ctx, `UPDATE users SET security_version=security_version+CASE WHEN password_hash!=? OR role!=? OR enabled!=? OR totp_enabled!=? THEN 1 ELSE 0 END, username=?, nickname=?, password_hash=?, role=?, enabled=?, avatar=?, totp_secret=?, totp_enabled=?, totp_last_step=?, recovery_codes=?, updated_at=? WHERE id=? AND updated_at=?`,
		u.PasswordHash, u.Role, b2i(u.Enabled), b2i(u.TOTPEnabled), u.Username, u.Nickname, u.PasswordHash, u.Role, b2i(u.Enabled), u.Avatar, s.seal("users.totp_secret", u.TOTPSecret), b2i(u.TOTPEnabled), u.TOTPLastStep, recoveryJSON(u.RecoveryCodes), fmtTime(now), u.ID, fmtTime(u.UpdatedAt))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("用户状态已变化，请刷新后重试")
	}
	u.UpdatedAt = now
	return err
}

// TouchLogin records a successful login without bumping updated_at (which
// versions the avatar URL).
func (s *Store) TouchLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, fmtTime(s.Now()), id)
	return err
}

// DeleteUser removes a user and its sessions.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// GetUser fetches by id.
func (s *Store) GetUser(ctx context.Context, id int64) (domain.User, error) {
	u, err := s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
	if isNoRows(err) {
		return u, ErrNotFound
	}
	return u, err
}

// GetUserByName fetches by username.
func (s *Store) GetUserByName(ctx context.Context, name string) (domain.User, error) {
	u, err := s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username=?`, name))
	if isNoRows(err) {
		return u, ErrNotFound
	}
	return u, err
}

// ListUsers returns all users ordered by id.
func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Session is a browser login session.
type Session struct {
	ID        string
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
	UserAgent string
	IP        string
}

// CreateSession stores a session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	return s.createSession(ctx, sess, nil)
}
func (s *Store) CreateAuthorizedSession(ctx context.Context, sess Session, u domain.User) error {
	return s.createSession(ctx, sess, &u)
}
func (s *Store) createSession(ctx context.Context, sess Session, u *domain.User) error {
	query := `INSERT INTO sessions(id,user_id,created_at,expires_at,user_agent,ip,security_version) SELECT ?,id,?,?,?,?,security_version FROM users WHERE id=? AND enabled=1`
	args := []any{sess.ID, fmtTime(sess.CreatedAt), fmtTime(sess.ExpiresAt), sess.UserAgent, sess.IP, sess.UserID}
	if u != nil {
		query += ` AND security_version=? AND password_hash=?`
		args = append(args, u.SecurityVersion, u.PasswordHash)
	}
	res, e := s.db.ExecContext(ctx, query, args...)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

// GetSession returns a session if it exists and is not expired.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	var sess Session
	var created, expires string
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id,created_at,expires_at,user_agent,ip FROM sessions WHERE id=? AND security_version=(SELECT security_version FROM users WHERE users.id=sessions.user_id AND enabled=1)`, id).
		Scan(&sess.ID, &sess.UserID, &created, &expires, &sess.UserAgent, &sess.IP)
	if isNoRows(err) {
		return sess, ErrNotFound
	}
	if err != nil {
		return sess, err
	}
	sess.CreatedAt = parseTime(created)
	sess.ExpiresAt = parseTime(expires)
	if sess.ExpiresAt.Before(s.Now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=? AND security_version=(SELECT security_version FROM users WHERE users.id=sessions.user_id AND enabled=1)`, id)
		return sess, ErrNotFound
	}
	return sess, nil
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=? AND security_version=(SELECT security_version FROM users WHERE users.id=sessions.user_id AND enabled=1)`, id)
	return err
}

// DeleteUserSessions logs a user out everywhere.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// PurgeExpiredSessions deletes stale sessions.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, fmtTime(s.Now()))
	return err
}

var _ = sql.ErrNoRows

// UpdateUserSecurity atomically consumes a factor without overwriting concurrent profile edits.
func (s *Store) UpdateUserSecurity(ctx context.Context, before, after domain.User) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET security_version=security_version+?,totp_secret=?,totp_enabled=?,totp_last_step=?,recovery_codes=?,updated_at=? WHERE id=? AND password_hash=? AND enabled=? AND updated_at=? AND totp_enabled=? AND totp_last_step=? AND recovery_codes=?`, b2i(before.TOTPEnabled != after.TOTPEnabled || (before.TOTPEnabled && before.TOTPSecret != after.TOTPSecret)), s.seal("users.totp_secret", after.TOTPSecret), b2i(after.TOTPEnabled), after.TOTPLastStep, recoveryJSON(after.RecoveryCodes), fmtTime(s.Now()), before.ID, before.PasswordHash, b2i(before.Enabled), fmtTime(before.UpdatedAt), b2i(before.TOTPEnabled), before.TOTPLastStep, recoveryJSON(before.RecoveryCodes))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("验证状态已变化，请重试")
	}
	return nil
}
