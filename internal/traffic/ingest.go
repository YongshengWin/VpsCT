// Package traffic turns cumulative agent counters into per-subject deltas,
// keeps the 30-day history and evaluates quotas.
package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
)

// ShareDelta is emitted when a share consumed traffic in a heartbeat.
type ShareDelta struct {
	ShareID int64
	Up      int64
	Down    int64
}

// Result summarises one ingested heartbeat.
type Result struct {
	ServerUp   int64
	ServerDown int64
	NodeDeltas map[int64][2]int64
	Shares     []ShareDelta
	Reset      bool // baseline was (re)established, no deltas produced
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

// delta computes the monotonic difference against the stored state and
// updates it. ok=false means a baseline was established (no delta).
func (i *Ingestor) delta(ctx context.Context, serverID int64, key, epoch string, rx, tx int64, now time.Time) (dRx, dTx int64, ok bool, err error) {
	st, found, err := i.Store.GetCounterState(ctx, serverID, key)
	if err != nil {
		return 0, 0, false, err
	}
	next := store.CounterState{Epoch: epoch, LastRx: rx, LastTx: tx, UpdatedAt: now}
	if !found || st.Epoch != epoch {
		// New baseline. Agent restart / nft recreate changes epoch but NIC
		// counters keep climbing — do not dump the boot-time total as traffic.
		// Only count the current reading when counters look reset (reboot).
		if found && st.Epoch != epoch && (rx < st.LastRx || tx < st.LastTx) {
			dRx, dTx, ok = rx, tx, true
		}
		return dRx, dTx, ok, i.Store.PutCounterState(ctx, serverID, key, next)
	}
	dRx, dTx = rx-st.LastRx, tx-st.LastTx
	if dRx < 0 || dTx < 0 {
		// counter wrapped or was reset without an epoch change: take the
		// current value as the delta (best effort) and rebase.
		dRx, dTx = rx, tx
	}
	return dRx, dTx, true, i.Store.PutCounterState(ctx, serverID, key, next)
}

// Ingest processes a heartbeat for the given server.
func (i *Ingestor) Ingest(ctx context.Context, server domain.Server, hb agentproto.Heartbeat) (Result, error) {
	now := i.Now()
	ts := hb.TS
	if ts.IsZero() || ts.After(now.Add(5*time.Minute)) || ts.Before(now.Add(-24*time.Hour)) {
		ts = now
	}
	res := Result{NodeDeltas: map[int64][2]int64{}}
	epoch := hb.Epoch
	if epoch == "" {
		epoch = "default"
	}

	// NIC totals -> server subject
	if hb.Metrics.NetRx > 0 || hb.Metrics.NetTx > 0 {
		dRx, dTx, ok, err := i.delta(ctx, server.ID, "nic", epoch, hb.Metrics.NetRx, hb.Metrics.NetTx, now)
		if err != nil {
			return res, err
		}
		_ = i.Store.AddSample(ctx, domain.TrafficSample{ServerID: server.ID, TS: ts, RxBytes: hb.Metrics.NetRx, TxBytes: hb.Metrics.NetTx})
		if ok {
			res.ServerUp, res.ServerDown = dRx, dTx
			if err := i.Store.AddTraffic(ctx, store.SubjectServer, server.ID, ts, dRx, dTx); err != nil {
				return res, err
			}
		} else {
			res.Reset = true
		}
	}

	if len(hb.Ports) == 0 {
		return res, nil
	}
	nodes, err := i.Store.ListNodes(ctx, store.NodeFilter{ServerID: &server.ID, IncludeRevoked: true})
	if err != nil {
		return res, err
	}
	byPort := map[int]domain.Node{}
	for _, n := range nodes {
		if n.ListenPort > 0 {
			byPort[n.ListenPort] = n
		}
	}
	shareAgg := map[int64]*ShareDelta{}
	for _, pc := range hb.Ports {
		key := fmt.Sprintf("port:%d", pc.Port)
		dRx, dTx, ok, err := i.delta(ctx, server.ID, key, epoch, pc.Rx, pc.Tx, now)
		if err != nil {
			return res, err
		}
		n, known := byPort[pc.Port]
		if !known {
			continue
		}
		nid := n.ID
		_ = i.Store.AddSample(ctx, domain.TrafficSample{ServerID: server.ID, NodeID: &nid, TS: ts, RxBytes: pc.Rx, TxBytes: pc.Tx})
		if !ok || (dRx == 0 && dTx == 0) {
			continue
		}
		res.NodeDeltas[n.ID] = [2]int64{dRx, dTx}
		if err := i.Store.AddTraffic(ctx, store.SubjectNode, n.ID, ts, dRx, dTx); err != nil {
			return res, err
		}
		if n.ShareID != nil {
			agg, ok := shareAgg[*n.ShareID]
			if !ok {
				agg = &ShareDelta{ShareID: *n.ShareID}
				shareAgg[*n.ShareID] = agg
			}
			agg.Up += dRx
			agg.Down += dTx
		}
	}
	for _, agg := range shareAgg {
		if err := i.Store.AddTraffic(ctx, store.SubjectShare, agg.ShareID, ts, agg.Up, agg.Down); err != nil {
			return res, err
		}
		res.Shares = append(res.Shares, *agg)
	}
	return res, nil
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
	Up          int64     `json:"up"`  // inbound (NIC rx)
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
	Subject string                 `json:"subject"`
	ID      int64                  `json:"id"`
	From    string                 `json:"from"`
	To      string                 `json:"to"`
	Points  []domain.TrafficBucket `json:"points"`
	TotalUp   int64                `json:"total_up"`
	TotalDown int64                `json:"total_down"`
}

// Daily returns a gap-filled daily series for the last `days` days.
func (i *Ingestor) Daily(ctx context.Context, subject string, id int64, days int) (Series, error) {
	now := i.Now()
	if days <= 0 {
		days = 30
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
	s := Series{Subject: subject, ID: id, From: from.Format("2006-01-02"), To: now.Format("2006-01-02")}
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
	return s, nil
}

// MetricsFromAgent decodes the stored metrics JSON.
func MetricsFromAgent(a domain.Agent) agentproto.Metrics {
	var m agentproto.Metrics
	_ = json.Unmarshal(a.Metrics, &m)
	return m
}
