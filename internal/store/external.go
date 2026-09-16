package store

import (
	"context"
	"database/sql"
	"time"

	"ctlvps/internal/domain"
)

const extCols = `id, name, url, user_agent, sync_interval_min, last_sync_at, last_error, upload, download, total, expire_at, node_count, enabled, owner_user_id, created_at, updated_at`

func (s *Store) scanExt(sc interface{ Scan(...any) error }) (domain.ExternalSubscription, error) {
	var e domain.ExternalSubscription
	var lastSync, expire sql.NullString
	var enabled int
	var created, updated string
	if err := sc.Scan(&e.ID, &e.Name, s.scanSecret("external_subscriptions.url", &e.URL), &e.UserAgent, &e.SyncIntervalMin, &lastSync, &e.LastError, &e.Upload, &e.Download, &e.Total, &expire,
		&e.NodeCount, &enabled, &e.OwnerUserID, &created, &updated); err != nil {
		return e, err
	}
	e.LastSyncAt = parseTimePtr(lastSync)
	e.ExpireAt = parseTimePtr(expire)
	e.Enabled = enabled == 1
	e.CreatedAt = parseTime(created)
	e.UpdatedAt = parseTime(updated)
	return e, nil
}

// CreateExternal inserts an external subscription.
func (s *Store) CreateExternal(ctx context.Context, e *domain.ExternalSubscription) error {
	now := s.Now()
	if e.SyncIntervalMin <= 0 {
		e.SyncIntervalMin = 360
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO external_subscriptions(name,url,user_agent,sync_interval_min,enabled,owner_user_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`,
		e.Name, s.seal("external_subscriptions.url", e.URL), e.UserAgent, e.SyncIntervalMin, b2i(e.Enabled), e.OwnerUserID, fmtTime(now), fmtTime(now))
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	e.CreatedAt, e.UpdatedAt = now, now
	return nil
}

// UpdateExternal saves editable fields.
func (s *Store) UpdateExternal(ctx context.Context, e *domain.ExternalSubscription) error {
	now := s.Now()
	_, err := s.db.ExecContext(ctx, `UPDATE external_subscriptions SET name=?, url=?, user_agent=?, sync_interval_min=?, enabled=?, updated_at=? WHERE id=?`,
		e.Name, s.seal("external_subscriptions.url", e.URL), e.UserAgent, e.SyncIntervalMin, b2i(e.Enabled), fmtTime(now), e.ID)
	e.UpdatedAt = now
	return err
}

// ExternalSyncResult is written after a fetch attempt.
type ExternalSyncResult struct {
	Err        string
	Upload     int64
	Download   int64
	Total      int64
	ExpireAt   *time.Time
	RawContent string
	NodeCount  int
	HasInfo    bool
}

// RecordExternalSync stores the result of a sync.
func (s *Store) RecordExternalSync(ctx context.Context, id int64, r ExternalSyncResult) error {
	now := fmtTime(s.Now())
	if r.Err != "" {
		_, err := s.db.ExecContext(ctx, `UPDATE external_subscriptions SET last_sync_at=?, last_error=?, updated_at=? WHERE id=?`, now, r.Err, now, id)
		return err
	}
	if r.HasInfo {
		_, err := s.db.ExecContext(ctx, `UPDATE external_subscriptions SET last_sync_at=?, last_error='', upload=?, download=?, total=?, expire_at=?, raw_content=?, node_count=?, updated_at=? WHERE id=?`,
			now, r.Upload, r.Download, r.Total, fmtTimePtr(r.ExpireAt), s.seal("external_subscriptions.raw_content", r.RawContent), r.NodeCount, now, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE external_subscriptions SET last_sync_at=?, last_error='', raw_content=?, node_count=?, updated_at=? WHERE id=?`,
		now, s.seal("external_subscriptions.raw_content", r.RawContent), r.NodeCount, now, id)
	return err
}

// ExternalRawContent returns the last fetched body.
func (s *Store) ExternalRawContent(ctx context.Context, id int64) (string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT raw_content FROM external_subscriptions WHERE id=?`, id).Scan(s.scanSecret("external_subscriptions.raw_content", &raw))
	if isNoRows(err) {
		return "", ErrNotFound
	}
	return raw, err
}

// DeleteExternal removes an external subscription and its nodes.
func (s *Store) DeleteExternal(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM external_subscriptions WHERE id=?`, id)
	return err
}

// GetExternal fetches one.
func (s *Store) GetExternal(ctx context.Context, id int64) (domain.ExternalSubscription, error) {
	e, err := s.scanExt(s.db.QueryRowContext(ctx, `SELECT `+extCols+` FROM external_subscriptions WHERE id=?`, id))
	if isNoRows(err) {
		return e, ErrNotFound
	}
	return e, err
}

// ListExternal returns all external subscriptions.
func (s *Store) ListExternal(ctx context.Context) ([]domain.ExternalSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+extCols+` FROM external_subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ExternalSubscription{}
	for rows.Next() {
		e, err := s.scanExt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
