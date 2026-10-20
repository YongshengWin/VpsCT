package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"ctlvps/internal/domain"
)

const shareCols = `id, name, user_id, targets, extra_node_ids, quota_bytes, billing_mode, reset_day, expires_at, status, template_id, connlog_enabled, notes, subscription_id, period_start, used_upload, used_download, created_at, updated_at, node_usage`

func scanShare(sc interface{ Scan(...any) error }) (domain.Share, error) {
	var v domain.Share
	var userID, templateID, subID sql.NullInt64
	var expires sql.NullString
	var targets, extra, period, created, updated, nodeUsage string
	var connlog int
	if err := sc.Scan(&v.ID, &v.Name, &userID, &targets, &extra, &v.QuotaBytes, &v.BillingMode, &v.ResetDay, &expires, &v.Status, &templateID, &connlog,
		&v.Notes, &subID, &period, &v.UsedUpload, &v.UsedDownload, &created, &updated, &nodeUsage); err != nil {
		return v, err
	}
	v.UserID = intPtr(userID)
	v.TemplateID = intPtr(templateID)
	v.SubscriptionID = intPtr(subID)
	v.ExpiresAt = parseTimePtr(expires)
	v.Targets = jsonList[domain.ShareTarget](targets)
	if err := json.Unmarshal([]byte(nodeUsage), &v.NodeUsage); err != nil {
		return v, err
	}
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
	if !domain.ValidTrafficTotals(v.UsedUpload, v.UsedDownload) {
		return errors.New("invalid share traffic totals")
	}
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
	if !domain.ValidTrafficTotals(v.UsedUpload, v.UsedDownload) {
		return errors.New("invalid share traffic totals")
	}
	now := s.Now()
	args := append(shareArgs(v), fmtTime(now), v.ID)
	_, err := s.db.ExecContext(ctx, `UPDATE shares SET name=?,user_id=?,targets=?,extra_node_ids=?,quota_bytes=?,billing_mode=?,reset_day=?,expires_at=?,status=?,template_id=?,connlog_enabled=?,notes=?,subscription_id=?,period_start=?,used_upload=?,used_download=?,updated_at=? WHERE id=?`, args...)
	v.UpdatedAt = now
	return err
}

// UpdateShareSettings saves management fields without replacing counters,
// period state, status or the subscription association with a stale read.
func (s *Store) UpdateShareSettings(ctx context.Context, v *domain.Share) error {
	return s.updateShareSettings(ctx, s.db, v)
}

func (s *Store) updateShareSettings(ctx context.Context, q querier, v *domain.Share) error {
	if v.Targets == nil {
		v.Targets = []domain.ShareTarget{}
	}
	if v.ExtraNodeIDs == nil {
		v.ExtraNodeIDs = []int64{}
	}
	now := s.Now()
	_, err := q.ExecContext(ctx, `UPDATE shares SET name=?,user_id=?,targets=?,extra_node_ids=?,quota_bytes=?,billing_mode=?,reset_day=?,expires_at=?,template_id=?,connlog_enabled=?,notes=?,updated_at=? WHERE id=?`,
		v.Name, nullInt(v.UserID), jsonStr(v.Targets), jsonStr(v.ExtraNodeIDs), v.QuotaBytes, v.BillingMode, v.ResetDay, fmtTimePtr(v.ExpiresAt), nullInt(v.TemplateID), b2i(v.ConnlogEnabled), v.Notes, fmtTime(now), v.ID)
	if err == nil {
		v.UpdatedAt = now
	}
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
	if !domain.ValidTrafficTotals(up, down) {
		return 0, 0, errors.New("invalid share traffic delta")
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT used_upload,used_download FROM shares WHERE id=?`, id).Scan(&usedUp, &usedDown); err != nil {
			return err
		}
		if !domain.ValidTrafficTotals(usedUp, usedDown) || !domain.ValidTrafficTotals(domain.Total(usedUp, usedDown), domain.Total(up, down)) {
			return errors.New("share traffic total overflow")
		}
		usedUp, usedDown = usedUp+up, usedDown+down
		if _, err := tx.ExecContext(ctx, `UPDATE shares SET used_upload=?, used_download=?, updated_at=? WHERE id=?`, usedUp, usedDown, fmtTime(s.Now()), id); err != nil {
			return err
		}
		return nil
	})
	return
}

// ResetShareUsage starts a new billing period.
func (s *Store) ResetShareUsage(ctx context.Context, id int64, periodStart time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shares SET node_usage='{}', used_upload=0, used_download=0, period_start=?, updated_at=? WHERE id=?`, fmtTime(periodStart), fmtTime(s.Now()), id)
	return err
}

// ResetShareUsageIfBefore advances a calendar period only if it is still due.
// A concurrent heartbeat may already have advanced it and committed new usage;
// a changed reset setting or a manual reset must also retain its newer state.
func (s *Store) ResetShareUsageIfBefore(ctx context.Context, id int64, resetDay int, periodStart time.Time) (changed bool, err error) {
	if resetDay <= 0 || periodStart.IsZero() {
		return false, nil
	}
	now := s.Now()
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var currentPeriod string
		var currentResetDay int
		if err := tx.QueryRowContext(ctx, `SELECT reset_day,period_start FROM shares WHERE id=?`, id).Scan(&currentResetDay, &currentPeriod); err != nil {
			return err
		}
		if currentResetDay != resetDay || !periodStart.After(parseTime(currentPeriod)) {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE shares SET node_usage='{}',used_upload=0,used_download=0,period_start=?,status=CASE WHEN status='exhausted' THEN 'active' ELSE status END,updated_at=? WHERE id=?`, fmtTime(periodStart), fmtTime(now), id)
		changed = err == nil
		return err
	})
	if err != nil {
		changed = false
	}
	return
}

// SetShareEvaluatedStatus commits an automatic transition only while the
// settings, counters and manual status used to compute it are unchanged.
func (s *Store) SetShareEvaluatedStatus(ctx context.Context, snapshot domain.Share, next domain.ShareStatus) (bool, error) {
	if snapshot.Status == domain.SharePaused || snapshot.Status == domain.ShareRevoked {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE shares SET status=?,updated_at=? WHERE id=? AND status=? AND billing_mode=? AND quota_bytes=? AND reset_day=? AND expires_at IS ? AND used_upload=? AND used_download=? AND period_start=?`,
		next, fmtTime(s.Now()), snapshot.ID, snapshot.Status, snapshot.BillingMode, snapshot.QuotaBytes, snapshot.ResetDay, fmtTimePtr(snapshot.ExpiresAt), snapshot.UsedUpload, snapshot.UsedDownload, fmtTime(snapshot.PeriodStart))
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	return changed == 1, err
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
