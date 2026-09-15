package store

import (
	"context"
	"database/sql"
	"time"

	"ctlvps/internal/domain"
)

const shareCols = `id, name, user_id, targets, extra_node_ids, quota_bytes, billing_mode, reset_day, expires_at, status, template_id, connlog_enabled, notes, subscription_id, period_start, used_upload, used_download, created_at, updated_at`

func scanShare(sc interface{ Scan(...any) error }) (domain.Share, error) {
	var v domain.Share
	var userID, templateID, subID sql.NullInt64
	var expires sql.NullString
	var targets, extra, period, created, updated string
	var connlog int
	if err := sc.Scan(&v.ID, &v.Name, &userID, &targets, &extra, &v.QuotaBytes, &v.BillingMode, &v.ResetDay, &expires, &v.Status, &templateID, &connlog,
		&v.Notes, &subID, &period, &v.UsedUpload, &v.UsedDownload, &created, &updated); err != nil {
		return v, err
	}
	v.UserID = intPtr(userID)
	v.TemplateID = intPtr(templateID)
	v.SubscriptionID = intPtr(subID)
	v.ExpiresAt = parseTimePtr(expires)
	v.Targets = jsonList[domain.ShareTarget](targets)
	v.ExtraNodeIDs = jsonList[int64](extra)
	v.ConnlogEnabled = connlog == 1
	v.PeriodStart = parseTime(period)
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, nil
}

func shareArgs(v *domain.Share) []any {
	if v.Targets == nil {
		v.Targets = []domain.ShareTarget{}
	}
	if v.ExtraNodeIDs == nil {
		v.ExtraNodeIDs = []int64{}
	}
	if v.BillingMode == "" {
		v.BillingMode = domain.BillingDual
	}
	if v.Status == "" {
		v.Status = domain.ShareActive
	}
	return []any{v.Name, nullInt(v.UserID), jsonStr(v.Targets), jsonStr(v.ExtraNodeIDs), v.QuotaBytes, v.BillingMode, v.ResetDay, fmtTimePtr(v.ExpiresAt), v.Status,
		nullInt(v.TemplateID), b2i(v.ConnlogEnabled), v.Notes, nullInt(v.SubscriptionID), fmtTime(v.PeriodStart), v.UsedUpload, v.UsedDownload}
}

// CreateShare inserts a share.
func (s *Store) CreateShare(ctx context.Context, v *domain.Share) error {
	now := s.Now()
	if v.PeriodStart.IsZero() {
		v.PeriodStart = now
	}
	args := append(shareArgs(v), fmtTime(now), fmtTime(now))
	res, err := s.db.ExecContext(ctx, `INSERT INTO shares(name,user_id,targets,extra_node_ids,quota_bytes,billing_mode,reset_day,expires_at,status,template_id,connlog_enabled,notes,subscription_id,period_start,used_upload,used_download,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	v.CreatedAt, v.UpdatedAt = now, now
	return nil
}

// UpdateShare saves all fields.
func (s *Store) UpdateShare(ctx context.Context, v *domain.Share) error {
	now := s.Now()
	args := append(shareArgs(v), fmtTime(now), v.ID)
	_, err := s.db.ExecContext(ctx, `UPDATE shares SET name=?,user_id=?,targets=?,extra_node_ids=?,quota_bytes=?,billing_mode=?,reset_day=?,expires_at=?,status=?,template_id=?,connlog_enabled=?,notes=?,subscription_id=?,period_start=?,used_upload=?,used_download=?,updated_at=? WHERE id=?`, args...)
	v.UpdatedAt = now
	return err
}

// DeleteShare removes a share (its subscription cascades, nodes are unlinked).
func (s *Store) DeleteShare(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE id=?`, id)
	return err
}

// GetShare fetches one.
func (s *Store) GetShare(ctx context.Context, id int64) (domain.Share, error) {
	v, err := scanShare(s.db.QueryRowContext(ctx, `SELECT `+shareCols+` FROM shares WHERE id=?`, id))
	if isNoRows(err) {
		return v, ErrNotFound
	}
	return v, err
}

// ListShares returns all shares; when userID != nil only that user's.
func (s *Store) ListShares(ctx context.Context, userID *int64) ([]domain.Share, error) {
	q := `SELECT ` + shareCols + ` FROM shares`
	var args []any
	if userID != nil {
		q += ` WHERE user_id=?`
		args = append(args, *userID)
	}
	q += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Share{}
	for rows.Next() {
		v, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AddShareUsage atomically adds traffic to the current period and returns the
// new totals.
func (s *Store) AddShareUsage(ctx context.Context, id, up, down int64) (usedUp, usedDown int64, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE shares SET used_upload=used_upload+?, used_download=used_download+?, updated_at=? WHERE id=?`, up, down, fmtTime(s.Now()), id); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT used_upload, used_download FROM shares WHERE id=?`, id).Scan(&usedUp, &usedDown)
	})
	return
}

// ResetShareUsage starts a new billing period.
func (s *Store) ResetShareUsage(ctx context.Context, id int64, periodStart time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shares SET used_upload=0, used_download=0, period_start=?, updated_at=? WHERE id=?`, fmtTime(periodStart), fmtTime(s.Now()), id)
	return err
}

// SetShareStatus updates only the status.
func (s *Store) SetShareStatus(ctx context.Context, id int64, st domain.ShareStatus) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shares SET status=?, updated_at=? WHERE id=?`, st, fmtTime(s.Now()), id)
	return err
}

// ShareEvent is a lifecycle log line.
type ShareEvent struct {
	ID      int64     `json:"id"`
	ShareID int64     `json:"share_id"`
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind"`
	Detail  string    `json:"detail"`
}

// AddShareEvent appends a lifecycle event.
func (s *Store) AddShareEvent(ctx context.Context, shareID int64, kind, detail string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO share_events(share_id,ts,kind,detail) VALUES (?,?,?,?)`, shareID, fmtTime(s.Now()), kind, detail)
	return err
}

// ListShareEvents returns recent events, newest first.
func (s *Store) ListShareEvents(ctx context.Context, shareID int64, limit int) ([]ShareEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, share_id, ts, kind, detail FROM share_events WHERE share_id=? ORDER BY id DESC LIMIT ?`, shareID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareEvent{}
	for rows.Next() {
		var e ShareEvent
		var ts string
		if err := rows.Scan(&e.ID, &e.ShareID, &ts, &e.Kind, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = parseTime(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}
