// Package traffic turns cumulative agent counters into per-subject deltas,
// keeps the 30-day history and evaluates quotas.
package traffic

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
)

// ShareDelta is emitted when a share consumed traffic in a heartbeat.
type ShareDelta struct {
	PeriodReset bool
	ShareID     int64
	Up          int64
	Down        int64
}

// Result summarises one ingested heartbeat.
type Result struct {
	ForwardReceiptAck string
	NetworkBillingAck string
	FinalMeterAck     string
	ServerUp          int64
	ServerDown        int64
	NodeDeltas        map[int64][2]int64
	Shares            []ShareDelta
	Reset             bool // baseline was (re)established, no deltas produced
}

// Ingestor applies heartbeats to the store.
type Ingestor struct {
	Store *store.Store
	Now   func() time.Time
}

// New builds an Ingestor.
func New(st *store.Store) *Ingestor {
	return &Ingestor{Store: st, Now: func() time.Time { return time.Now().UTC() }}
}

// Ingest processes a heartbeat for the given server.
func (i *Ingestor) Ingest(ctx context.Context, server domain.Server, hb agentproto.Heartbeat) (Result, error) {
	if r := hb.ForwardReceipt; r != nil {
		if err := r.Validate(); err != nil {
			return Result{}, err
		}
		if len(hb.ForwardCounters) != 0 || len(hb.Ports) != 0 || hb.FinalMeters != nil || hb.NetworkBillingSwitch != nil || hb.NetworkBillingLegacy != nil {
			return Result{}, errors.New("转发回执不能混入其他计量报告")
		}
		hb.ForwardCounters, hb.TS, hb.Metrics = r.Counters, r.TS, agentproto.Metrics{}
	}
	if err := agentproto.ValidateForwardCounters(hb.ForwardCounters); err != nil {
		return Result{}, err
	}
	if len(hb.ForwardCounters) > 0 && (hb.FinalMeters != nil || hb.NetworkBillingSwitch != nil) {
		return Result{}, errors.New("forward counters must not mix node settlement or network billing cutover")
	}
	if sw := hb.NetworkBillingSwitch; sw != nil {
		if err := sw.Validate(); err != nil {
			return Result{}, err
		}
		if hb.FinalMeters != nil || len(hb.Ports) != 0 {
			return Result{}, errors.New("billing switch must not mix node counters")
		}
		hb.TS, hb.Epoch = sw.TS, sw.Legacy.Epoch
		hb.Metrics = agentproto.Metrics{NetRx: sw.Legacy.Rx, NetTx: sw.Legacy.Tx, Network: sw.Snapshot}
	} else if hb.NetworkBillingLegacy != nil {
		if err := hb.NetworkBillingLegacy.Validate(); err != nil {
			return Result{}, err
		}
		hb.Epoch = hb.NetworkBillingLegacy.Epoch
		hb.Metrics.NetRx, hb.Metrics.NetTx = hb.NetworkBillingLegacy.Rx, hb.NetworkBillingLegacy.Tx
	}
	if hb.FinalMeters != nil {
		if err := hb.FinalMeters.Validate(); err != nil {
			return Result{}, err
		}
		if len(hb.Ports) != 0 {
			return Result{}, errors.New("final snapshot must not mix live counters")
		}
		hb.Ports = hb.FinalMeters.Counters
		hb.TS = hb.FinalMeters.TS
		hb.Metrics = agentproto.Metrics{}
	}
	now := i.Now()
	ts := hb.TS
	if ts.IsZero() || ts.After(now.Add(5*time.Minute)) {
		ts = now
	}
	res := Result{NodeDeltas: map[int64][2]int64{}}
	nodes, err := i.Store.ListNodes(ctx, store.NodeFilter{ServerID: &server.ID, IncludeRevoked: true})
	if err != nil {
		return res, err
	}
	byID, byPort := map[int64]domain.Node{}, map[int]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
		if !n.Revoked {
			byPort[n.ListenPort] = n
		}
	}
	identities, err := i.Store.MeterNodes(ctx, server.ID)
	if err != nil {
		return res, err
	}
	for _, n := range identities {
		if _, ok := byID[n.ID]; !ok {
			byID[n.ID] = n
		}
	}
	epoch := hb.Epoch
	if epoch == "" {
		epoch = "default"
	}
	err = i.Store.Tx(ctx, func(tx *sql.Tx) error {
		var appliedForwards []agentproto.ForwardSpec
		if r := hb.ForwardReceipt; r != nil {
			replay, applied, err := i.Store.CheckForwardReceipt(ctx, tx, server.ID, *r)
			if err != nil {
				return err
			}
			if replay {
				res.ForwardReceiptAck = r.ID
				return nil
			}
			appliedForwards = applied
		}
		if hb.FinalMeters != nil {
			var exists int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM meter_settlements WHERE server_id=? AND batch_id=?", server.ID, hb.FinalMeters.ID).Scan(&exists)
			if err == nil {
				res.FinalMeterAck = hb.FinalMeters.ID
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			for _, pc := range hb.Ports {
				if _, ok := byID[pc.NodeID]; !ok {
					return errors.New("unknown final meter identity")
				}
			}
		}
		add := func(subject string, id int64, rx, txBytes int64) error {
			for _, bucket := range []struct{ table, key string }{{"traffic_hourly", ts.UTC().Truncate(time.Hour).Format(time.RFC3339)}, {"traffic_daily", ts.UTC().Format("2006-01-02")}} {
				_, err := tx.ExecContext(ctx, "INSERT INTO "+bucket.table+"(bucket,subject,subject_id,up,down) VALUES (?,?,?,?,?) ON CONFLICT(bucket,subject,subject_id) DO UPDATE SET up=up+excluded.up,down=down+excluded.down", bucket.key, subject, id, rx, txBytes)
				if err != nil {
					return err
				}
			}
			return nil
		}
		delta := func(key, ep string, rx, txBytes int64, fromZero bool) (int64, int64, bool, error) {
			if rx < 0 || txBytes < 0 || rx > math.MaxInt64/4 || txBytes > math.MaxInt64/4 {
				return 0, 0, false, errors.New("invalid traffic counters")
			}
			var oldEpoch, updated string
			var oldRx, oldTx int64
			err := tx.QueryRowContext(ctx, "SELECT epoch,last_rx,last_tx,updated_at FROM counter_state WHERE server_id=? AND counter_key=?", server.ID, key).Scan(&oldEpoch, &oldRx, &oldTx, &updated)
			found := err == nil
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, 0, false, err
			}
			// Stale/replayed reports never rewind the accounting baseline.
			baselineTS := ts
			if found {
				if last, e := time.Parse(time.RFC3339Nano, updated); e == nil && ts.Before(last) {
					if hb.FinalMeters == nil && hb.NetworkBillingSwitch == nil && hb.ForwardReceipt == nil {
						return 0, 0, false, nil
					}
					// A frozen final snapshot may follow a wall-clock correction.
					// Accept only monotonic counters of the same generation; never
					// ACK and silently discard unsettled bytes as a stale heartbeat.
					if oldEpoch != ep || rx < oldRx || txBytes < oldTx {
						return 0, 0, false, errors.New("final meter conflicts with newer baseline")
					}
					baselineTS = last
				}
			}
			dr, dt, ok := int64(0), int64(0), false
			if found && (oldEpoch == ep || (key == "nic" && strings.Split(oldEpoch, ":")[0] == strings.Split(ep, ":")[0])) {
				if rx < oldRx || txBytes < oldTx {
					if fromZero {
						return 0, 0, false, errors.New("counter reset without a new epoch")
					}
					dr, dt = rx, txBytes
				} else {
					dr, dt = rx-oldRx, txBytes-oldTx
				}
				ok = true
			} else if fromZero || (found && (rx < oldRx || txBytes < oldTx)) {
				dr, dt, ok = rx, txBytes, true
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO counter_state(server_id,counter_key,epoch,last_rx,last_tx,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(server_id,counter_key) DO UPDATE SET epoch=excluded.epoch,last_rx=excluded.last_rx,last_tx=excluded.last_tx,updated_at=excluded.updated_at`, server.ID, key, ep, rx, txBytes, baselineTS.UTC().Format(time.RFC3339Nano))
			return dr, dt, ok, err
		}
		sample := func(node any, rx, txBytes int64) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO traffic_samples(server_id,node_id,ts,rx_bytes,tx_bytes) VALUES(?,?,?,?,?)", server.ID, node, ts.UTC().Format(time.RFC3339Nano), rx, txBytes)
			if err != nil {
				return err
			}
			if nodeID, ok := node.(int64); ok {
				return add(store.SubjectNode, nodeID, 0, 0)
			}
			return add(store.SubjectServer, server.ID, 0, 0)
		}
		legacy := func() error {
			if hb.Metrics.NetRx > 0 || hb.Metrics.NetTx > 0 {
				rx, txBytes, ok, err := delta("nic", epoch, hb.Metrics.NetRx, hb.Metrics.NetTx, false)
				if err != nil {
					return err
				}
				if ok {
					res.ServerUp, res.ServerDown = rx, txBytes
					if err = add(store.SubjectServer, server.ID, rx, txBytes); err != nil {
						return err
					}
				} else {
					res.Reset = true
				}
			}
			return nil
		}
		if hb.FinalMeters == nil && hb.ForwardReceipt == nil {
			if err := i.ingestNetworkBilling(ctx, tx, server.ID, hb, ts, &res, legacy, add, sample); err != nil {
				return err
			}
			if hb.NetworkBillingSwitch != nil {
				return nil
			}
		}
		if err := ingestForwardCounters(ctx, tx, server.ID, hb.ForwardCounters, delta, add); err != nil {
			return err
		}
		if r := hb.ForwardReceipt; r != nil {
			if err := store.CommitForwardReceipt(ctx, tx, server.ID, *r, appliedForwards, now); err != nil {
				return err
			}
			res.ForwardReceiptAck = r.ID
			return nil
		}
		seen := map[string]bool{}
		shares := map[int64]*ShareDelta{}
		for _, pc := range hb.Ports {
			n, known := byPort[pc.Port]
			key := fmt.Sprintf("port:%d", pc.Port)
			ep := epoch
			if pc.NodeID > 0 {
				n, known = byID[pc.NodeID]
				if pc.Source != "nft-node-v1" && pc.Source != "systemd-v1" && pc.Source != "systemd-legacy-v1" {
					return errors.New("unknown node meter source")
				}
				if pc.Epoch == "" || len(pc.Epoch) > 128 {
					return errors.New("missing node meter epoch")
				}
				key = fmt.Sprintf("node:%d:%s:%s", pc.NodeID, pc.Source, pc.Epoch)
				ep = pc.Epoch
			}
			if !known {
				continue
			}
			if seen[key] {
				return errors.New("duplicate node counter")
			}
			seen[key] = true
			rx, txBytes, ok, err := delta(key, ep, pc.Rx, pc.Tx, pc.FromZero)
			if err != nil {
				return err
			}
			if n.Source == domain.NodeTransit {
				if ok {
					if err = add("transit", n.ID, rx, txBytes); err != nil {
						return err
					}
				}
				continue
			}
			if err = sample(n.ID, pc.Rx, pc.Tx); err != nil {
				return err
			}
			if !ok || (rx == 0 && txBytes == 0) {
				continue
			}
			prev := res.NodeDeltas[n.ID]
			res.NodeDeltas[n.ID] = [2]int64{prev[0] + rx, prev[1] + txBytes}
			if err = add(store.SubjectNode, n.ID, rx, txBytes); err != nil {
				return err
			}
			if n.ShareID != nil {
				p := shares[*n.ShareID]
				if p == nil {
					p = &ShareDelta{ShareID: *n.ShareID}
					shares[*n.ShareID] = p
				}
				p.Up += rx
				p.Down += txBytes
			}
		}
		for _, sh := range shares {
			if err := add(store.SubjectShare, sh.ShareID, sh.Up, sh.Down); err != nil {
				return err
			}
			// Usage and its counter baseline commit together. Quota evaluation is
			// retryable and must never add these deltas a second time.
			var resetDay int
			var period string
			if err := tx.QueryRowContext(ctx, "SELECT reset_day,period_start FROM shares WHERE id=?", sh.ShareID).Scan(&resetDay, &period); err != nil {
				return err
			}
			oldPeriod, _ := time.Parse(time.RFC3339Nano, period)
			start := PeriodStart(now, resetDay)
			if !start.IsZero() && start.After(oldPeriod) {
				if _, err := tx.ExecContext(ctx, "UPDATE shares SET period_start=?,used_upload=0,used_download=0,status=CASE WHEN status='exhausted' THEN 'active' ELSE status END WHERE id=?", start.Format(time.RFC3339Nano), sh.ShareID); err != nil {
					return err
				}
				sh.PeriodReset = true
			}
			// Delayed durable reports belong to their sampling period. Keep
			// their historical ledger, but do not consume the current quota.
			if !start.IsZero() && ts.Before(start) {
				sh.Up, sh.Down = 0, 0
				res.Shares = append(res.Shares, *sh)
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE shares SET used_upload=used_upload+?,used_download=used_download+? WHERE id=?", sh.Up, sh.Down, sh.ShareID); err != nil {
				return err
			}
			res.Shares = append(res.Shares, *sh)
		}
		if hb.FinalMeters != nil {
			if _, err := tx.ExecContext(ctx, "INSERT INTO meter_settlements(server_id,batch_id,created_at) VALUES(?,?,?)", server.ID, hb.FinalMeters.ID, now.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
			res.FinalMeterAck = hb.FinalMeters.ID
		}
		return nil
	})
	if err != nil {
		res.FinalMeterAck = ""
		res.ForwardReceiptAck = ""
	}
	return res, err
}

// PeriodStart returns the start of the current billing period.
// 1–28 are that calendar day; 29/30/31 mean the last day of each month.
func PeriodStart(now time.Time, resetDay int) time.Time {
	resetDay = domain.NormalizeResetDay(resetDay)
	if resetDay <= 0 {
		return time.Time{}
	}
	now = now.UTC()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	thisMonth := clampResetDate(y, m, resetDay)
	if today.Before(thisMonth) {
		return clampResetDate(y, m-1, resetDay)
	}
	return thisMonth
}

// NextReset returns the next period boundary after now.
func NextReset(now time.Time, resetDay int) time.Time {
	start := PeriodStart(now, resetDay)
	if start.IsZero() {
		return time.Time{}
	}
	y, m, _ := start.Date()
	return clampResetDate(y, m+1, resetDay)
}

func clampResetDate(year int, month time.Month, resetDay int) time.Time {
	resetDay = domain.NormalizeResetDay(resetDay)
	if resetDay < 1 {
		resetDay = 1
	}
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if resetDay >= 29 || resetDay > last {
		resetDay = last
	}
	return time.Date(year, month, resetDay, 0, 0, 0, 0, time.UTC)
}

// ServerUsage is the current-period usage of a VPS.
type ServerUsage struct {
	ServerID    int64     `json:"server_id"`
	PeriodStart time.Time `json:"period_start"`
	Up          int64     `json:"up"`   // inbound (NIC rx)
	Down        int64     `json:"down"` // outbound (NIC tx)
	Inbound     int64     `json:"inbound"`
	Outbound    int64     `json:"outbound"`
	Total       int64     `json:"total"`
	Billed      int64     `json:"billed"`
	OneWay      int64     `json:"one_way"`
	TwoWay      int64     `json:"two_way"`
	Quota       int64     `json:"quota"`
	Percent     float64   `json:"percent"`
	OverQuota   bool      `json:"over_quota"`
}

// ServerUsage computes the current period usage of a server.
func (i *Ingestor) ServerUsage(ctx context.Context, s domain.Server) (ServerUsage, error) {
	now := i.Now()
	start := PeriodStart(now, s.QuotaResetDay)
	if start.IsZero() {
		start = now.AddDate(0, 0, -30)
	}
	up, down, err := i.Store.SumTraffic(ctx, store.SubjectServer, s.ID, start, now)
	if err != nil {
		return ServerUsage{}, err
	}
	u := ServerUsage{ServerID: s.ID, PeriodStart: start, Up: up, Down: down, Quota: s.QuotaBytes}
	u.Inbound = domain.Inbound(up, down)
	u.Outbound = domain.Outbound(up, down)
	u.Total = domain.Total(up, down)
	u.OneWay = u.Outbound
	u.TwoWay = u.Total
	u.Billed = u.Total
	if s.QuotaBytes > 0 {
		u.Percent = float64(u.Billed) / float64(s.QuotaBytes) * 100
		u.OverQuota = u.Billed >= s.QuotaBytes
	}
	return u, nil
}

// Series is a chart-ready daily series.
type Series struct {
	HasData   bool                   `json:"has_data"`
	Total     int64                  `json:"total"`
	Subject   string                 `json:"subject"`
	ID        int64                  `json:"id"`
	From      string                 `json:"from"`
	To        string                 `json:"to"`
	Points    []domain.TrafficBucket `json:"points"`
	TotalUp   int64                  `json:"total_up"`
	TotalDown int64                  `json:"total_down"`
}

// Daily returns a gap-filled daily series for the last `days` days.
func (i *Ingestor) Daily(ctx context.Context, subject string, id int64, days int) (Series, error) {
	now := i.Now()
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	from := now.AddDate(0, 0, -(days - 1)).Truncate(24 * time.Hour)
	var rows []domain.TrafficBucket
	var err error
	if id == 0 {
		rows, err = i.Store.DailyTotals(ctx, subject, from, now)
	} else {
		rows, err = i.Store.DailyTraffic(ctx, subject, id, from, now)
	}
	if err != nil {
		return Series{}, err
	}
	byDay := map[string]domain.TrafficBucket{}
	for _, r := range rows {
		byDay[r.Bucket.Format("2006-01-02")] = r
	}
	s := Series{HasData: len(rows) > 0, Subject: subject, ID: id, From: from.Format("2006-01-02"), To: now.Format("2006-01-02")}
	for d := from; !d.After(now); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		b := domain.TrafficBucket{Bucket: d}
		if r, ok := byDay[key]; ok {
			b.Up, b.Down = r.Up, r.Down
		}
		s.TotalUp += b.Up
		s.TotalDown += b.Down
		s.Points = append(s.Points, b)
	}
	s.Total = s.TotalUp + s.TotalDown
	return s, nil
}

// MetricsFromAgent decodes the stored metrics JSON.
func MetricsFromAgent(a domain.Agent) agentproto.Metrics {
	var m agentproto.Metrics
	_ = json.Unmarshal(a.Metrics, &m)
	return m
}
