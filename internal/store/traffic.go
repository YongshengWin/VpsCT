package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"ctlvps/internal/domain"
)

// Subject kinds for aggregated traffic rows.
const (
	SubjectServer   = "server"
	SubjectNode     = "node"
	SubjectExternal = "external"
	SubjectShare    = "share"
)

// CounterState is the last cumulative reading for one counter.
type CounterState struct {
	Epoch     string
	LastRx    int64
	LastTx    int64
	UpdatedAt time.Time
}

// GetCounterState returns the stored state for (server, key).
func (s *Store) GetCounterState(ctx context.Context, serverID int64, key string) (CounterState, bool, error) {
	var st CounterState
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT epoch, last_rx, last_tx, updated_at FROM counter_state WHERE server_id=? AND counter_key=?`, serverID, key).
		Scan(&st.Epoch, &st.LastRx, &st.LastTx, &updated)
	if isNoRows(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	st.UpdatedAt = parseTime(updated)
	return st, true, nil
}

// PutCounterState upserts the state.
func (s *Store) PutCounterState(ctx context.Context, serverID int64, key string, st CounterState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO counter_state(server_id,counter_key,epoch,last_rx,last_tx,updated_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(server_id,counter_key) DO UPDATE SET epoch=excluded.epoch, last_rx=excluded.last_rx, last_tx=excluded.last_tx, updated_at=excluded.updated_at`,
		serverID, key, st.Epoch, st.LastRx, st.LastTx, fmtTime(st.UpdatedAt))
	return err
}

// AddSample stores a raw cumulative reading.
func (s *Store) AddSample(ctx context.Context, sm domain.TrafficSample) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO traffic_samples(server_id,node_id,ts,rx_bytes,tx_bytes) VALUES (?,?,?,?,?)`,
		sm.ServerID, nullInt(sm.NodeID), fmtTime(sm.TS), sm.RxBytes, sm.TxBytes)
	return err
}

// ListSamples returns raw samples for a server (node_id NULL) or a node.
func (s *Store) ListSamples(ctx context.Context, serverID int64, nodeID *int64, since time.Time) ([]domain.TrafficSample, error) {
	var rows *sql.Rows
	var err error
	if nodeID == nil {
		rows, err = s.db.QueryContext(ctx, `SELECT server_id,node_id,ts,rx_bytes,tx_bytes FROM traffic_samples WHERE server_id=? AND node_id IS NULL AND ts>=? ORDER BY ts`, serverID, fmtTime(since))
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT server_id,node_id,ts,rx_bytes,tx_bytes FROM traffic_samples WHERE server_id=? AND node_id=? AND ts>=? ORDER BY ts`, serverID, *nodeID, fmtTime(since))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TrafficSample{}
	for rows.Next() {
		var sm domain.TrafficSample
		var nid sql.NullInt64
		var ts string
		if err := rows.Scan(&sm.ServerID, &nid, &ts, &sm.RxBytes, &sm.TxBytes); err != nil {
			return nil, err
		}
		sm.NodeID = intPtr(nid)
		sm.TS = parseTime(ts)
		out = append(out, sm)
	}
	return out, rows.Err()
}

// AddTraffic adds deltas to the hourly and daily buckets of subject/id.
func (s *Store) AddTraffic(ctx context.Context, subject string, id int64, at time.Time, up, down int64) error {
	if up == 0 && down == 0 {
		return nil
	}
	at = at.UTC()
	hour := at.Truncate(time.Hour).Format(time.RFC3339)
	day := at.Format("2006-01-02")
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_hourly(bucket,subject,subject_id,up,down) VALUES (?,?,?,?,?)
			ON CONFLICT(bucket,subject,subject_id) DO UPDATE SET up=up+excluded.up, down=down+excluded.down`, hour, subject, id, up, down); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO traffic_daily(bucket,subject,subject_id,up,down) VALUES (?,?,?,?,?)
			ON CONFLICT(bucket,subject,subject_id) DO UPDATE SET up=up+excluded.up, down=down+excluded.down`, day, subject, id, up, down)
		return err
	})
}

// SetDailyTraffic overwrites the daily bucket (used for external subscriptions
// whose usage is reported as absolute totals by the upstream).
func (s *Store) SetDailyTraffic(ctx context.Context, subject string, id int64, day time.Time, up, down int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO traffic_daily(bucket,subject,subject_id,up,down) VALUES (?,?,?,?,?)
		ON CONFLICT(bucket,subject,subject_id) DO UPDATE SET up=excluded.up, down=excluded.down`, day.UTC().Format("2006-01-02"), subject, id, up, down)
	return err
}

// DailyTraffic returns per-day buckets for one subject in [from, to].
func (s *Store) DailyTraffic(ctx context.Context, subject string, id int64, from, to time.Time) ([]domain.TrafficBucket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bucket, up, down FROM traffic_daily WHERE subject=? AND subject_id=? AND bucket>=? AND bucket<=? ORDER BY bucket`,
		subject, id, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TrafficBucket{}
	for rows.Next() {
		var b domain.TrafficBucket
		var day string
		if err := rows.Scan(&day, &b.Up, &b.Down); err != nil {
			return nil, err
		}
		b.Bucket, _ = time.Parse("2006-01-02", day)
		out = append(out, b)
	}
	return out, rows.Err()
}

// HourlyTraffic returns hourly buckets for one subject since from.
func (s *Store) HourlyTraffic(ctx context.Context, subject string, id int64, from time.Time) ([]domain.TrafficBucket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bucket, up, down FROM traffic_hourly WHERE subject=? AND subject_id=? AND bucket>=? ORDER BY bucket`,
		subject, id, from.UTC().Truncate(time.Hour).Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TrafficBucket{}
	for rows.Next() {
		var b domain.TrafficBucket
		var h string
		if err := rows.Scan(&h, &b.Up, &b.Down); err != nil {
			return nil, err
		}
		b.Bucket, _ = time.Parse(time.RFC3339, h)
		out = append(out, b)
	}
	return out, rows.Err()
}

// SumTraffic totals a subject in [from, to] (inclusive days).
func (s *Store) SumTraffic(ctx context.Context, subject string, id int64, from, to time.Time) (up, down int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(up),0), COALESCE(SUM(down),0) FROM traffic_daily WHERE subject=? AND subject_id=? AND bucket>=? AND bucket<=?`,
		subject, id, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")).Scan(&up, &down)
	return
}

// SumTrafficIDs totals one subject across many IDs in [from, to].
func (s *Store) SumTrafficIDs(ctx context.Context, subject string, ids []int64, from, to time.Time) (up, down int64, err error) {
	if len(ids) == 0 {
		return 0, 0, nil
	}
	args := make([]any, 0, len(ids)+3)
	args = append(args, subject)
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	args = append(args, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(up),0), COALESCE(SUM(down),0) FROM traffic_daily WHERE subject=? AND subject_id IN (`+strings.Join(ph, ",")+`) AND bucket>=? AND bucket<=?`, args...).Scan(&up, &down)
	return
}

// DailyTotals sums all subjects of one kind per day (dashboard chart).
func (s *Store) DailyTotals(ctx context.Context, subject string, from, to time.Time) ([]domain.TrafficBucket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bucket, SUM(up), SUM(down) FROM traffic_daily WHERE subject=? AND bucket>=? AND bucket<=? GROUP BY bucket ORDER BY bucket`,
		subject, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TrafficBucket{}
	for rows.Next() {
		var b domain.TrafficBucket
		var day string
		if err := rows.Scan(&day, &b.Up, &b.Down); err != nil {
			return nil, err
		}
		b.Bucket, _ = time.Parse("2006-01-02", day)
		out = append(out, b)
	}
	return out, rows.Err()
}

// PruneTraffic enforces retention.
func (s *Store) PruneTraffic(ctx context.Context, sampleRetention, hourlyRetention time.Duration) error {
	now := s.Now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM traffic_samples WHERE ts < ?`, fmtTime(now.Add(-sampleRetention))); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM traffic_hourly WHERE bucket < ?`, now.Add(-hourlyRetention).Truncate(time.Hour).Format(time.RFC3339))
	return err
}

// TrafficSummary is a measured window, not an inferred usage estimate.
type TrafficSummary struct {
	Inbound     int64  `json:"inbound"`
	Outbound    int64  `json:"outbound"`
	Total       int64  `json:"total"`
	Days        int    `json:"days"`
	HasData     bool   `json:"has_data"`
	FirstSample string `json:"first_sample,omitempty"`
	LastSample  string `json:"last_sample,omitempty"`
}

// NodeTrafficSummaries uses two grouped queries, avoiding a query per node.
func (s *Store) NodeTrafficSummaries(ctx context.Context, days int) (map[int64]TrafficSummary, error) {
	out := map[int64]TrafficSummary{}
	since := s.Now().UTC().AddDate(0, 0, -(days - 1)).Truncate(24 * time.Hour)
	rows, err := s.db.QueryContext(ctx, `SELECT subject_id,SUM(up),SUM(down) FROM traffic_daily WHERE subject='node' AND bucket>=? GROUP BY subject_id`, since.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, rx, tx int64
		if err = rows.Scan(&id, &rx, &tx); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = TrafficSummary{Inbound: rx, Outbound: tx, Total: rx + tx, Days: days, HasData: true}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT node_id,MIN(ts),MAX(ts) FROM traffic_samples WHERE node_id IS NOT NULL AND ts>=? GROUP BY node_id`, fmtTime(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var first, last string
		if err = rows.Scan(&id, &first, &last); err != nil {
			return nil, err
		}
		v := out[id]
		v.Days, v.HasData, v.FirstSample, v.LastSample = days, true, first, last
		out[id] = v
	}
	return out, rows.Err()
}

// MeterNodes returns identities including removed nodes, without credentials.
func (s *Store) MeterNodes(ctx context.Context, serverID int64) ([]domain.Node, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT node_id,listen_port,core,share_id FROM node_meter_identities WHERE server_id=?", serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Node
	for rows.Next() {
		var n domain.Node
		var share sql.NullInt64
		if err := rows.Scan(&n.ID, &n.ListenPort, &n.Core, &share); err != nil {
			return nil, err
		}
		n.ServerID = &serverID
		n.ShareID = intPtr(share)
		out = append(out, n)
	}
	return out, rows.Err()
}
