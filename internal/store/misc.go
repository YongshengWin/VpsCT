package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

// ---- desired states ----

const desiredCols = `id, server_id, revision, payload, hash, status, error, created_at, applied_at`

func (s *Store) scanDesired(sc interface{ Scan(...any) error }) (domain.DesiredState, error) {
	var d domain.DesiredState
	var payload, created string
	var applied sql.NullString
	if err := sc.Scan(&d.ID, &d.ServerID, &d.Revision, s.scanSecret("desired_states.payload", &payload), &d.Hash, &d.Status, &d.Error, &created, &applied); err != nil {
		return d, err
	}
	d.Payload = rawOrEmpty(payload)
	d.CreatedAt = parseTime(created)
	d.AppliedAt = parseTimePtr(applied)
	return d, nil
}

// CreateDesiredState appends a new revision for a server and marks older
// pending revisions as superseded.
func (s *Store) CreateDesiredState(ctx context.Context, serverID int64, payload []byte, hash string) (domain.DesiredState, error) {
	var d domain.DesiredState
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var rev int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM desired_states WHERE server_id=?`, serverID).Scan(&rev); err != nil {
			return err
		}
		rev++
		if _, err := tx.ExecContext(ctx, `UPDATE desired_states SET status=? WHERE server_id=? AND status=?`, domain.DesiredStale, serverID, domain.DesiredPending); err != nil {
			return err
		}
		now := s.Now()
		res, err := tx.ExecContext(ctx, `INSERT INTO desired_states(server_id,revision,payload,hash,status,created_at) VALUES (?,?,?,?,?,?)`,
			serverID, rev, s.seal("desired_states.payload", string(payload)), hash, domain.DesiredPending, fmtTime(now))
		if err != nil {
			return err
		}
		d.ID, _ = res.LastInsertId()
		d.ServerID, d.Revision, d.Payload, d.Hash, d.Status, d.CreatedAt = serverID, rev, payload, hash, domain.DesiredPending, now
		return nil
	})
	return d, err
}

var ErrDesiredConflict = errors.New("desired state changed while building")

// PublishDesired atomically embeds the assigned revision and compares with
// the revision observed BEFORE building the candidate. An older build cannot
// overwrite a completed newer publish; the caller must rebuild after conflict.
func (s *Store) PublishDesired(ctx context.Context, expected int64, candidate *agentproto.DesiredState, force bool) (domain.DesiredState, bool, error) {
	var out domain.DesiredState
	created := false
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		generation, err := networkGeneration(ctx, tx, candidate.ServerID)
		if err != nil {
			return err
		}
		if generation != candidate.NetworkGeneration {
			return ErrDesiredConflict
		}
		if generation > 0 {
			if err := networkMaintenance(ctx, tx, candidate.ServerID); err != nil {
				return err
			}
		}
		latest, err := s.scanDesired(tx.QueryRowContext(ctx, `SELECT `+desiredCols+` FROM desired_states WHERE server_id=? ORDER BY revision DESC LIMIT 1`, candidate.ServerID))
		if err != nil && !isNoRows(err) {
			return err
		}
		if latest.Revision != expected {
			return ErrDesiredConflict
		}
		var republish bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM network_operations WHERE server_id=? AND generation=? AND republish=1 AND status IN ('queued','publish_failed'))`, candidate.ServerID, generation).Scan(&republish); err != nil {
			return err
		}
		force = force || republish
		if latest.Revision > 0 && latest.Hash == candidate.Hash && !force {
			out = latest
			return s.attachNetworkOperations(ctx, tx, out, generation)
		}
		ds := *candidate
		ds.Revision = expected + 1
		payload, err := json.Marshal(&ds)
		if err != nil {
			return err
		}
		now := s.Now()
		if _, err := tx.ExecContext(ctx, `UPDATE desired_states SET status=? WHERE server_id=? AND status=?`, domain.DesiredStale, ds.ServerID, domain.DesiredPending); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO desired_states(server_id,revision,payload,hash,status,created_at) VALUES (?,?,?,?,?,?)`, ds.ServerID, ds.Revision, s.seal("desired_states.payload", string(payload)), ds.Hash, domain.DesiredPending, fmtTime(now))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out = domain.DesiredState{ID: id, ServerID: ds.ServerID, Revision: ds.Revision, Payload: payload, Hash: ds.Hash, Status: domain.DesiredPending, CreatedAt: now}
		created = true
		return s.attachNetworkOperations(ctx, tx, out, generation)
	})
	if err != nil {
		if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
			err = ErrDesiredConflict
		}
		return domain.DesiredState{}, false, err
	}
	return out, created, nil
}

// LatestDesiredState returns the newest revision for a server.
func (s *Store) LatestDesiredState(ctx context.Context, serverID int64) (domain.DesiredState, error) {
	d, err := s.scanDesired(s.db.QueryRowContext(ctx, `SELECT `+desiredCols+` FROM desired_states WHERE server_id=? ORDER BY revision DESC LIMIT 1`, serverID))
	if isNoRows(err) {
		return d, ErrNotFound
	}
	return d, err
}

// MarkDesiredState records agent feedback on a revision.
func (s *Store) MarkDesiredState(ctx context.Context, serverID, revision int64, st domain.DesiredStateStatus, errMsg string) error {
	var appliedAt any
	if st != domain.DesiredPending {
		appliedAt = fmtTime(s.Now())
	}
	_, err := s.db.ExecContext(ctx, `UPDATE desired_states SET status=?, error=?, applied_at=? WHERE server_id=? AND revision=?`, st, errMsg, appliedAt, serverID, revision)
	return err
}

// ListDesiredStates returns recent revisions for a server.
func (s *Store) ListDesiredStates(ctx context.Context, serverID int64, limit int) ([]domain.DesiredState, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+desiredCols+` FROM desired_states WHERE server_id=? ORDER BY revision DESC LIMIT ?`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DesiredState{}
	for rows.Next() {
		d, err := s.scanDesired(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PruneDesiredStates keeps the latest n revisions per server.
func (s *Store) PruneDesiredStates(ctx context.Context, keep int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM desired_states WHERE id IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY server_id ORDER BY revision DESC) AS rn FROM desired_states) WHERE rn > ?)`, keep)
	return err
}

// ---- audit ----

// AddAudit appends an audit event.
func (s *Store) AddAudit(ctx context.Context, e domain.AuditEvent) error {
	return s.addAudit(ctx, s.db, e)
}

func (s *Store) addAudit(ctx context.Context, q querier, e domain.AuditEvent) error {
	if len(e.Target) > 512 {
		e.Target = e.Target[:512]
	}
	if len(e.Detail) > 4096 {
		e.Detail = []byte(`{"truncated":true}`)
	}

	if e.TS.IsZero() {
		e.TS = s.Now()
	}
	if len(e.Detail) == 0 {
		e.Detail = []byte("{}")
	}
	_, err := q.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,target,detail,ip) VALUES (?,?,?,?,?,?,?)`,
		fmtTime(e.TS), nullInt(e.UserID), e.Username, e.Action, e.Target, string(e.Detail), e.IP)
	return err
}

// ListAudit returns recent events, newest first.
func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]domain.AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, user_id, username, action, target, detail, ip FROM audit_log ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AuditEvent{}
	for rows.Next() {
		var e domain.AuditEvent
		var ts, detail string
		var uid sql.NullInt64
		if err := rows.Scan(&e.ID, &ts, &uid, &e.Username, &e.Action, &e.Target, &detail, &e.IP); err != nil {
			return nil, err
		}
		e.TS = parseTime(ts)
		e.UserID = intPtr(uid)
		e.Detail = rawOrEmpty(detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- access log ----

// AddAccessLog records a subscription fetch.
func (s *Store) AddAccessLog(ctx context.Context, l domain.AccessLog) error {
	if l.TS.IsZero() {
		l.TS = s.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_log(subscription_id,ts,ip,user_agent,format,status) VALUES (?,?,?,?,?,?)`,
		l.SubscriptionID, fmtTime(l.TS), l.IP, l.UserAgent, l.Format, l.Status)
	return err
}

// ListAccessLog returns fetches, newest first, optionally for one subscription.
func (s *Store) ListAccessLog(ctx context.Context, subID *int64, limit, offset int) ([]domain.AccessLog, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, subscription_id, ts, ip, user_agent, format, status FROM access_log`
	var args []any
	if subID != nil {
		q += ` WHERE subscription_id=?`
		args = append(args, *subID)
	}
	q += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AccessLog{}
	for rows.Next() {
		var l domain.AccessLog
		var ts string
		if err := rows.Scan(&l.ID, &l.SubscriptionID, &ts, &l.IP, &l.UserAgent, &l.Format, &l.Status); err != nil {
			return nil, err
		}
		l.TS = parseTime(ts)
		out = append(out, l)
	}
	return out, rows.Err()
}

// CountAccessSince counts fetches from ip since t (rate limiting).
func (s *Store) CountAccessSince(ctx context.Context, ip string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_log WHERE ip=? AND ts>=?`, ip, fmtTime(t)).Scan(&n)
	return n, err
}

// PruneAccessLog enforces retention.
func (s *Store) PruneAccessLog(ctx context.Context, retention time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM access_log WHERE ts < ?`, fmtTime(s.Now().Add(-retention)))
	return err
}

// PruneAudit enforces retention.
func (s *Store) PruneAudit(ctx context.Context, retention time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE ts < ?`, fmtTime(s.Now().Add(-retention)))
	return err
}

// ---- ban rules ----

// CreateBan inserts a ban rule.
func (s *Store) CreateBan(ctx context.Context, b *domain.BanRule) error {
	now := s.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO ban_rules(kind,value,reason,enabled,expires_at,created_at) VALUES (?,?,?,?,?,?)`,
		b.Kind, b.Value, b.Reason, b2i(b.Enabled), fmtTimePtr(b.ExpiresAt), fmtTime(now))
	if err != nil {
		return err
	}
	b.ID, _ = res.LastInsertId()
	b.CreatedAt = now
	return nil
}

// UpdateBan saves a ban rule.
func (s *Store) UpdateBan(ctx context.Context, b *domain.BanRule) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ban_rules SET kind=?, value=?, reason=?, enabled=?, expires_at=? WHERE id=?`,
		b.Kind, b.Value, b.Reason, b2i(b.Enabled), fmtTimePtr(b.ExpiresAt), b.ID)
	return err
}

// DeleteBan removes a ban rule.
func (s *Store) DeleteBan(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ban_rules WHERE id=?`, id)
	return err
}

// ListBans returns all ban rules.
func (s *Store) ListBans(ctx context.Context) ([]domain.BanRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, value, reason, enabled, expires_at, created_at FROM ban_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.BanRule{}
	for rows.Next() {
		var b domain.BanRule
		var enabled int
		var exp sql.NullString
		var created string
		if err := rows.Scan(&b.ID, &b.Kind, &b.Value, &b.Reason, &enabled, &exp, &created); err != nil {
			return nil, err
		}
		b.Enabled = enabled == 1
		b.ExpiresAt = parseTimePtr(exp)
		b.CreatedAt = parseTime(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---- settings ----

// GetSetting returns a setting or def when missing.
func (s *Store) GetSetting(ctx context.Context, key, def string) string {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(s.scanSecret("settings.value", &v)); err != nil {
		return def
	}
	return v
}

// GetSettingInt returns a numeric setting.
func (s *Store) GetSettingInt(ctx context.Context, key string, def int) int {
	v := s.GetSetting(ctx, key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// GetSettingBool returns a boolean setting.
func (s *Store) GetSettingBool(ctx context.Context, key string, def bool) bool {
	v := s.GetSetting(ctx, key, "")
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// SetSetting upserts a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	return s.SetSettings(ctx, map[string]string{key: value})
}

// AllSettings returns every setting.
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, s.scanSecret("settings.value", &v)); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SettingsJSON returns settings as a JSON object (secrets masked).
func (s *Store) SettingsJSON(ctx context.Context) (json.RawMessage, error) {
	m, err := s.AllSettings(ctx)
	if err != nil {
		return nil, err
	}
	if v, ok := m[domain.SettingTelegramToken]; ok && len(v) > 8 {
		m[domain.SettingTelegramToken] = v[:4] + "…" + v[len(v)-4:]
	}
	return json.Marshal(m)
}
