package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ctlvps/internal/domain"
)

// ServerQuotaSnapshot is the policy and calibration used to evaluate an
// automatic stop. ObservedAt also guards rolling windows and calendar periods
// against a UTC day change between evaluation and the write.
type ServerQuotaSnapshot struct {
	Server      domain.Server
	ObservedAt  time.Time
	PeriodStart time.Time
	Calibration *ServerUsageCalibration
}

// DisableServerIfQuotaUnchanged changes no editable server settings. A newer
// administrator edit or calibration makes the decision stale; the next
// heartbeat will evaluate it again using the new settings.
func (s *Store) DisableServerIfQuotaUnchanged(ctx context.Context, snapshot ServerQuotaSnapshot, currentTime func() time.Time) (bool, error) {
	v := snapshot.Server
	if !v.Enabled || v.QuotaBytes <= 0 || snapshot.ObservedAt.IsZero() || snapshot.PeriodStart.IsZero() {
		return false, nil
	}
	if currentTime == nil {
		currentTime = s.Now
	}
	query := `UPDATE servers SET enabled=0,updated_at=?
		WHERE id=? AND enabled=1 AND quota_bytes=? AND quota_billing=? AND quota_reset_day=? AND updated_at=?`
	args := []any{nil, v.ID, v.QuotaBytes, v.QuotaBilling, v.QuotaResetDay, fmtTime(v.UpdatedAt)}
	if v.QuotaResetDay > 0 {
		period := snapshot.PeriodStart.UTC().Format("2006-01-02")
		if c := snapshot.Calibration; c != nil {
			var observedUp, observedDown, providerUp, providerDown any
			if c.HasDirectionalSnapshot {
				observedUp, observedDown = c.ObservedUpBytes, c.ObservedDownBytes
			}
			if c.HasProviderDirections {
				providerUp, providerDown = c.ProviderUpBytes, c.ProviderDownBytes
			}
			query += ` AND EXISTS (SELECT 1 FROM server_usage_calibrations c
				WHERE c.server_id=servers.id AND c.period_start=? AND c.calibrated_bytes=? AND c.observed_bytes=?
				AND c.observed_up_bytes IS ? AND c.observed_down_bytes IS ?
				AND c.provider_up_bytes IS ? AND c.provider_down_bytes IS ? AND c.calibrated_at=?)`
			args = append(args, period, c.CalibratedBytes, c.ObservedBytes,
				observedUp, observedDown, providerUp, providerDown, fmtTime(c.CalibratedAt))
		} else {
			query += ` AND NOT EXISTS (SELECT 1 FROM server_usage_calibrations c
				WHERE c.server_id=servers.id AND c.period_start=?)`
			args = append(args, period)
		}
	}
	day := snapshot.ObservedAt.UTC().Format("2006-01-02")
	staleDay := errors.New("server quota observation expired")
	changed := false
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if day != currentTime().UTC().Format("2006-01-02") {
			return nil
		}
		args[0] = fmtTime(s.Now())
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		// SQLite may have waited for a concurrent writer. Roll the change back
		// if that wait crossed the observation day's boundary.
		if day != currentTime().UTC().Format("2006-01-02") {
			return staleDay
		}
		changed = rows == 1
		return nil
	})
	if errors.Is(err, staleDay) {
		return false, nil
	}
	return changed && err == nil, err
}
