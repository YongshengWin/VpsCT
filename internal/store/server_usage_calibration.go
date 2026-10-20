package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"ctlvps/internal/domain"
)

// ServerUsageCalibration is a provider's current-period reading together with
// the observed amount under the active quota mode when that reading was saved.
type ServerUsageCalibration struct {
	CalibratedBytes        int64
	ObservedBytes          int64
	ObservedUpBytes        int64
	ObservedDownBytes      int64
	HasDirectionalSnapshot bool
	ProviderUpBytes        int64
	ProviderDownBytes      int64
	HasProviderDirections  bool
	CalibratedAt           time.Time
}

func saveServerUsageCalibrationTx(ctx context.Context, tx *sql.Tx, serverID int64, billingMode string, calibratedBytes *int64, periodStart, now time.Time, providerDirections ...[2]int64) error {
	if calibratedBytes == nil {
		if len(providerDirections) != 0 {
			return errors.New("provider directions require a calibrated amount")
		}
		return nil
	}
	if *calibratedBytes < 0 || periodStart.IsZero() {
		return errors.New("invalid server usage calibration")
	}
	var up, down int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(up),0), COALESCE(SUM(down),0)
		FROM traffic_daily WHERE subject='server' AND subject_id=? AND bucket>=? AND bucket<=?`,
		serverID, periodStart.UTC().Format("2006-01-02"), now.UTC().Format("2006-01-02")).Scan(&up, &down); err != nil {
		return err
	}
	if up < 0 || down < 0 || up > math.MaxInt64-down {
		return errors.New("server traffic total overflow")
	}
	observed := domain.ServerQuotaBilled(billingMode, up, down)
	var providerUp, providerDown any
	if len(providerDirections) > 1 {
		return errors.New("multiple provider direction snapshots")
	}
	if len(providerDirections) == 1 {
		p := providerDirections[0]
		if p[0] < 0 || p[1] < 0 || p[0] > math.MaxInt64-p[1] || domain.ServerQuotaBilled(billingMode, p[0], p[1]) != *calibratedBytes {
			return errors.New("invalid provider direction snapshot")
		}
		providerUp, providerDown = p[0], p[1]
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO server_usage_calibrations(server_id,period_start,calibrated_bytes,observed_bytes,observed_up_bytes,observed_down_bytes,provider_up_bytes,provider_down_bytes,calibrated_at)
		VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET period_start=excluded.period_start,
		calibrated_bytes=excluded.calibrated_bytes,observed_bytes=excluded.observed_bytes,
		observed_up_bytes=excluded.observed_up_bytes,observed_down_bytes=excluded.observed_down_bytes,
		provider_up_bytes=excluded.provider_up_bytes,provider_down_bytes=excluded.provider_down_bytes,
		calibrated_at=excluded.calibrated_at`,
		serverID, periodStart.UTC().Format("2006-01-02"), *calibratedBytes, observed, up, down, providerUp, providerDown, fmtTime(now))
	return err
}

// GetServerUsageCalibration returns a calibration only for the requested
// billing period. Prior-period readings remain in storage but are never billed.
func (s *Store) GetServerUsageCalibration(ctx context.Context, serverID int64, periodStart time.Time) (ServerUsageCalibration, bool, error) {
	var c ServerUsageCalibration
	if periodStart.IsZero() {
		return c, false, nil
	}
	var calibratedAt string
	var observedUp, observedDown sql.NullInt64
	var providerUp, providerDown sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT calibrated_bytes,observed_bytes,observed_up_bytes,observed_down_bytes,provider_up_bytes,provider_down_bytes,calibrated_at
		FROM server_usage_calibrations WHERE server_id=? AND period_start=?`,
		serverID, periodStart.UTC().Format("2006-01-02")).Scan(&c.CalibratedBytes, &c.ObservedBytes, &observedUp, &observedDown, &providerUp, &providerDown, &calibratedAt)
	if isNoRows(err) {
		return c, false, nil
	}
	if providerUp.Valid && providerDown.Valid {
		c.ProviderUpBytes, c.ProviderDownBytes = providerUp.Int64, providerDown.Int64
		c.HasProviderDirections = true
	}
	if err != nil {
		return c, false, err
	}
	if observedUp.Valid && observedDown.Valid {
		c.ObservedUpBytes = observedUp.Int64
		c.ObservedDownBytes = observedDown.Int64
		c.HasDirectionalSnapshot = true
	}
	c.CalibratedAt = parseTime(calibratedAt)
	return c, true, nil
}
