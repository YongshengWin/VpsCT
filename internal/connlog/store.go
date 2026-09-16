// Package connlog stores per-connection destination logs uploaded by agents
// in a dedicated SQLite database (connlog.db) so that the main database stays
// small and retention can be tuned independently.
package connlog

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

// Store is the connection log database.
type Store struct {
	db  *sql.DB
	Now func() time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS conn_batches(server_id INTEGER PRIMARY KEY, seq INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS conn_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  share_id INTEGER,
  ts TEXT NOT NULL,
  network TEXT NOT NULL,
  dest_host TEXT NOT NULL,
  dest_port INTEGER NOT NULL,
  src_host TEXT NOT NULL DEFAULT '',
  agent_seq INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conn_ts ON conn_events(ts);
CREATE INDEX IF NOT EXISTS idx_conn_share ON conn_events(share_id, ts);
CREATE INDEX IF NOT EXISTS idx_conn_node ON conn_events(node_id, ts);
CREATE INDEX IF NOT EXISTS idx_conn_host ON conn_events(dest_host);

CREATE TABLE IF NOT EXISTS conn_daily_domains (
  day TEXT NOT NULL,
  share_id INTEGER NOT NULL DEFAULT 0,
  node_id INTEGER NOT NULL DEFAULT 0,
  dest_host TEXT NOT NULL,
  hits INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, share_id, node_id, dest_host)
);
`

// Open opens/creates connlog.db.
func Open(path string) (*Store, error) {
	original := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		path = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=max_page_count(131072)&_pragma=journal_size_limit(16777216)"
	} else {
		path = "file::memory:?cache=shared"
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateConnEvents(db); err != nil {
		db.Close()
		return nil, err
	}
	if original != ":memory:" {
		for _, p := range []string{original, original + "-wal", original + "-shm"} {
			if e := os.Chmod(p, 0600); e != nil && !os.IsNotExist(e) {
				db.Close()
				return nil, e
			}
		}
	}
	return &Store{db: db, Now: func() time.Time { return time.Now().UTC() }}, nil
}

func migrateConnEvents(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('conn_events') WHERE name='src_host'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec(`ALTER TABLE conn_events ADD COLUMN src_host TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_conn_src ON conn_events(src_host)`)
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ingest stores a batch. shareOf maps node id -> share id (nil when none).
func (s *Store) Ingest(ctx context.Context, serverID int64, batch agentproto.ConnlogBatch, shareOf map[int64]*int64) (int, error) {
	if len(batch.Events) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if batch.Seq > 0 {
		var seq int64
		e := tx.QueryRowContext(ctx, "SELECT seq FROM conn_batches WHERE server_id=?", serverID).Scan(&seq)
		if e != nil && e != sql.ErrNoRows {
			return 0, e
		}
		if batch.Seq <= seq {
			return 0, nil
		}
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO conn_events(server_id,node_id,share_id,ts,network,dest_host,dest_port,src_host,agent_seq) VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	agg, err := tx.PrepareContext(ctx, `INSERT INTO conn_daily_domains(day,share_id,node_id,dest_host,hits) VALUES (?,?,?,?,1)
		ON CONFLICT(day,share_id,node_id,dest_host) DO UPDATE SET hits=hits+1`)
	if err != nil {
		return 0, err
	}
	defer agg.Close()
	n := 0
	now := s.Now()
	for _, e := range batch.Events {
		host := strings.ToLower(strings.TrimSpace(e.DestHost))
		if host == "" {
			continue
		}
		if len(host) > 253 {
			host = host[:253]
		}
		src := strings.TrimSpace(e.SrcHost)
		if len(src) > 45 {
			src = src[:45]
		}
		ts := e.TS
		if ts.IsZero() || ts.After(now.Add(10*time.Minute)) {
			ts = now
		}
		var shareID any
		var shareKey int64
		if sid, ok := shareOf[e.NodeID]; ok && sid != nil {
			shareID = *sid
			shareKey = *sid
		}
		if _, err := ins.ExecContext(ctx, serverID, e.NodeID, shareID, ts.UTC().Format(time.RFC3339), e.Network, host, e.DestPort, src, batch.Seq); err != nil {
			return n, err
		}
		if _, err := agg.ExecContext(ctx, ts.UTC().Format("2006-01-02"), shareKey, e.NodeID, host); err != nil {
			return n, err
		}
		n++
	}
	if batch.Seq > 0 {
		if _, e := tx.ExecContext(ctx, "INSERT INTO conn_batches(server_id,seq) VALUES(?,?) ON CONFLICT(server_id) DO UPDATE SET seq=excluded.seq", serverID, batch.Seq); e != nil {
			return n, e
		}
	}
	return n, tx.Commit()
}

// Filter narrows queries.
type Filter struct {
	ShareID  *int64
	SelfOnly bool // share_id IS NULL (自用)
	NodeID   *int64
	ServerID *int64
	Host     string // dest substring
	SrcHost  string // client substring
	From     time.Time
	To       time.Time
	Limit    int
	Offset   int
}

func (f Filter) where() (string, []any) {
	var w []string
	var args []any
	if f.SelfOnly {
		w = append(w, "share_id IS NULL")
	} else if f.ShareID != nil {
		w = append(w, "share_id=?")
		args = append(args, *f.ShareID)
	}
	if f.NodeID != nil {
		w = append(w, "node_id=?")
		args = append(args, *f.NodeID)
	}
	if f.ServerID != nil {
		w = append(w, "server_id=?")
		args = append(args, *f.ServerID)
	}
	if f.Host != "" {
		w = append(w, "dest_host LIKE ?")
		args = append(args, "%"+strings.ToLower(f.Host)+"%")
	}
	if f.SrcHost != "" {
		w = append(w, "src_host LIKE ?")
		args = append(args, "%"+f.SrcHost+"%")
	}
	if !f.From.IsZero() {
		w = append(w, "ts>=?")
		args = append(args, f.From.UTC().Format(time.RFC3339))
	}
	if !f.To.IsZero() {
		w = append(w, "ts<=?")
		args = append(args, f.To.UTC().Format(time.RFC3339))
	}
	if len(w) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(w, " AND "), args
}

// Query returns raw events, newest first.
func (s *Store) Query(ctx context.Context, f Filter) ([]domain.ConnEvent, error) {
	where, args := f.where()
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args = append(args, limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id, server_id, node_id, share_id, ts, network, dest_host, dest_port, src_host, agent_seq FROM conn_events`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ConnEvent{}
	for rows.Next() {
		var e domain.ConnEvent
		var sid sql.NullInt64
		var ts string
		if err := rows.Scan(&e.ID, &e.ServerID, &e.NodeID, &sid, &ts, &e.Network, &e.DestHost, &e.DestPort, &e.SrcHost, &e.AgentSeq); err != nil {
			return nil, err
		}
		if sid.Valid {
			v := sid.Int64
			e.ShareID = &v
		}
		e.TS, _ = time.Parse(time.RFC3339, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Count counts raw events matching the filter.
func (s *Store) Count(ctx context.Context, f Filter) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conn_events`+where, args...).Scan(&n)
	return n, err
}

// DomainHit is one row of the top-domains report.
type DomainHit struct {
	Host string `json:"host"`
	Hits int64  `json:"hits"`
}

// TopDomains aggregates the daily table for a share/node over [from, to].
func (s *Store) TopDomains(ctx context.Context, shareID, nodeID *int64, from, to time.Time, limit int) ([]DomainHit, error) {
	if limit <= 0 {
		limit = 50
	}
	var w []string
	var args []any
	if shareID != nil {
		w = append(w, "share_id=?")
		args = append(args, *shareID)
	}
	if nodeID != nil {
		w = append(w, "node_id=?")
		args = append(args, *nodeID)
	}
	if !from.IsZero() {
		w = append(w, "day>=?")
		args = append(args, from.UTC().Format("2006-01-02"))
	}
	if !to.IsZero() {
		w = append(w, "day<=?")
		args = append(args, to.UTC().Format("2006-01-02"))
	}
	q := `SELECT dest_host, SUM(hits) FROM conn_daily_domains`
	if len(w) > 0 {
		q += " WHERE " + strings.Join(w, " AND ")
	}
	q += ` GROUP BY dest_host ORDER BY SUM(hits) DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainHit{}
	for rows.Next() {
		var h DomainHit
		if err := rows.Scan(&h.Host, &h.Hits); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// CountRow is one grouped counter.
type CountRow struct {
	Key  string `json:"key"`
	Hits int64  `json:"hits"`
}

// GroupCount aggregates raw events by an allowlisted column.
func (s *Store) GroupCount(ctx context.Context, f Filter, col string, limit int) ([]CountRow, error) {
	expr := ""
	switch col {
	case "dest_host":
		expr = "dest_host"
	case "src_host":
		expr = "src_host"
	case "node_id":
		expr = "CAST(node_id AS TEXT)"
	case "server_id":
		expr = "CAST(server_id AS TEXT)"
	case "share_id":
		expr = "COALESCE(CAST(share_id AS TEXT),'self')"
	default:
		return nil, fmt.Errorf("unsupported group column")
	}
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	where, args := f.where()
	if col == "src_host" {
		if where == "" {
			where = " WHERE src_host!=''"
		} else {
			where += " AND src_host!=''"
		}
	}
	q := `SELECT ` + expr + `, COUNT(*) FROM conn_events` + where + ` GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CountRow{}
	for rows.Next() {
		var r CountRow
		if err := rows.Scan(&r.Key, &r.Hits); err != nil {
			return nil, err
		}
		if r.Key == "" {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ExportCSV streams matching events as CSV.
func (s *Store) ExportCSV(ctx context.Context, f Filter, w io.Writer) error {
	f.Limit = 1000
	f.Offset = 0
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"ts", "server_id", "node_id", "share_id", "network", "dest_host", "dest_port", "src_host"})
	for {
		rows, err := s.Query(ctx, f)
		if err != nil {
			return err
		}
		for _, e := range rows {
			sid := ""
			if e.ShareID != nil {
				sid = fmt.Sprint(*e.ShareID)
			}
			_ = cw.Write([]string{e.TS.Format(time.RFC3339), fmt.Sprint(e.ServerID), fmt.Sprint(e.NodeID), sid, e.Network, e.DestHost, fmt.Sprint(e.DestPort), e.SrcHost})
		}
		if len(rows) < f.Limit {
			break
		}
		f.Offset += f.Limit
		if f.Offset > 200000 {
			break
		}
	}
	cw.Flush()
	return cw.Error()
}

// Prune enforces retention for raw and aggregated rows.
func (s *Store) Prune(ctx context.Context, rawRetention, aggRetention time.Duration) error {
	now := s.Now()
	res, err := s.db.ExecContext(ctx, `DELETE FROM conn_events WHERE ts < ?`, now.Add(-rawRetention).Format(time.RFC3339))
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM conn_daily_domains WHERE day < ?`, now.Add(-aggRetention).Format("2006-01-02")); err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 1000 {
		_, _ = s.db.ExecContext(ctx, `VACUUM`)
	}
	return nil
}

// DeleteShare wipes all logs of a share (privacy / revoke).
func (s *Store) DeleteShare(ctx context.Context, shareID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM conn_events WHERE share_id=?`, shareID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM conn_daily_domains WHERE share_id=?`, shareID)
	return err
}

// Stats returns row counts and database size hints.
func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	var raw, agg, pageCount, pageSize int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conn_events`).Scan(&raw); err != nil {
		return nil, err
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conn_daily_domains`).Scan(&agg)
	_ = s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount)
	_ = s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize)
	out["raw_events"] = raw
	out["daily_rows"] = agg
	out["db_bytes"] = pageCount * pageSize
	return out, nil
}
