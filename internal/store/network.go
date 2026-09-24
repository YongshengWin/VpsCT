package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"ctlvps/internal/agentproto"
)

const SubjectInterface = "interface"

var ErrNetworkCapacity = errors.New("网卡观测身份记录已达到容量上限")

type InterfaceRecord struct {
	ID         int64                       `json:"id"`
	Interface  agentproto.NetworkInterface `json:"interface"`
	Archived   bool                        `json:"archived"`
	Present    bool                        `json:"present"`
	LastSeenAt time.Time                   `json:"last_seen_at"`
}

type NetworkView struct {
	Snapshot   *agentproto.NetworkSnapshot `json:"snapshot"`
	ReceivedAt *time.Time                  `json:"received_at"`
	Interfaces []InterfaceRecord           `json:"interfaces"`
}

// IngestNetwork atomically commits identity, baseline and observation history.
// Neither server nor node/share usage is touched. Retired sources cannot become
// current again, including delayed heartbeats from a previous boot or registry.
func (s *Store) IngestNetwork(ctx context.Context, serverID int64, snapshot *agentproto.NetworkSnapshot) error {
	if snapshot == nil {
		return nil
	}
	if err := agentproto.ValidateNetwork(snapshot); err != nil {
		return err
	}
	now := s.Now().UTC()
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var current string
		err := tx.QueryRowContext(ctx, "SELECT source FROM network_snapshots WHERE server_id=?", serverID).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		source := snapshot.CollectorID + ":" + snapshot.BootID
		if snapshot.Status == "ok" || snapshot.Status == "incomplete" {
			var sequence int64
			err = tx.QueryRowContext(ctx, "SELECT sequence FROM network_sources WHERE server_id=? AND source=?", serverID, source).Scan(&sequence)
			if err == nil && (current != source || snapshot.Sequence <= sequence) {
				return nil
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if errors.Is(err, sql.ErrNoRows) {
				// The registry sequence spans boots. Even an old boot which never
				// reached this controller cannot supersede a newer sample.
				var latest int64
				if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM network_sources WHERE server_id=? AND substr(source,1,32)=?", serverID, snapshot.CollectorID).Scan(&latest); err != nil {
					return err
				}
				if snapshot.Sequence <= latest {
					return nil
				}
				var count int
				if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM network_sources WHERE server_id=?", serverID).Scan(&count); err != nil {
					return err
				}
				if count >= 4096 {
					return ErrNetworkCapacity
				}
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO network_sources(server_id,source,sequence) VALUES (?,?,?) ON CONFLICT(server_id,source) DO UPDATE SET sequence=excluded.sequence`, serverID, source, snapshot.Sequence); err != nil {
				return err
			}
			if snapshot.Status == "ok" {
				if _, err = tx.ExecContext(ctx, "UPDATE network_interfaces SET present=0 WHERE server_id=?", serverID); err != nil {
					return err
				}
			}
			for _, n := range snapshot.Interfaces {
				var id, rx, out int64
				var epoch string
				var valid bool
				err := tx.QueryRowContext(ctx, "SELECT id,epoch,valid,rx,tx FROM network_interfaces WHERE server_id=? AND interface_id=?", serverID, n.ID).Scan(&id, &epoch, &valid, &rx, &out)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if errors.Is(err, sql.ErrNoRows) {
					var count int
					if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM network_interfaces WHERE server_id=?", serverID).Scan(&count); err != nil {
						return err
					}
					if count >= 2048 {
						return ErrNetworkCapacity
					}
					// Keep the first sample a baseline after the count query.
					err = sql.ErrNoRows
				}
				nextEpoch := source + ":" + n.Generation
				var inbound, outbound int64
				measured := err == nil && valid && n.CountersValid && epoch == nextEpoch && n.Rx >= rx && n.Tx >= out
				if measured {
					inbound, outbound = n.Rx-rx, n.Tx-out
				}
				payload, err := json.Marshal(n)
				if err != nil {
					return err
				}
				err = tx.QueryRowContext(ctx, `INSERT INTO network_interfaces(server_id,interface_id,snapshot,present,last_seen_at,epoch,valid,rx,tx) VALUES (?,?,?,1,?,?,?,?,?)
 ON CONFLICT(server_id,interface_id) DO UPDATE SET snapshot=excluded.snapshot,present=1,archived=0,last_seen_at=excluded.last_seen_at,epoch=excluded.epoch,valid=excluded.valid,rx=excluded.rx,tx=excluded.tx RETURNING id`, serverID, n.ID, string(payload), fmtTime(now), nextEpoch, n.CountersValid, n.Rx, n.Tx).Scan(&id)
				if err != nil {
					return err
				}
				if !measured {
					continue
				}
				for _, table := range []string{"traffic_hourly", "traffic_daily"} {
					bucket := now.Format("2006-01-02")
					if table == "traffic_hourly" {
						bucket = now.Truncate(time.Hour).Format(time.RFC3339)
					}
					if _, err = tx.ExecContext(ctx, `INSERT INTO `+table+`(bucket,subject,subject_id,up,down) VALUES (?,'interface',?,?,?) ON CONFLICT(bucket,subject,subject_id) DO UPDATE SET up=up+excluded.up,down=down+excluded.down`, bucket, id, inbound, outbound); err != nil {
						return err
					}
				}
			}
		} else {
			// A collector failure must retain the source used for replay protection.
			source = current
		}
		b, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO network_snapshots(server_id,source,snapshot,received_at) VALUES (?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET source=excluded.source,snapshot=excluded.snapshot,received_at=excluded.received_at`, serverID, source, string(b), fmtTime(now))
		return err
	})
}

func (s *Store) Network(ctx context.Context, serverID int64) (NetworkView, error) {
	v := NetworkView{Interfaces: []InterfaceRecord{}}
	var snapshot, received string
	err := s.db.QueryRowContext(ctx, "SELECT snapshot,received_at FROM network_snapshots WHERE server_id=?", serverID).Scan(&snapshot, &received)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal([]byte(snapshot), &v.Snapshot); err != nil {
		return v, err
	}
	at := parseTime(received)
	v.ReceivedAt = &at
	rows, err := s.db.QueryContext(ctx, "SELECT id,snapshot,present,last_seen_at FROM network_interfaces WHERE server_id=? AND archived=0 ORDER BY present DESC,last_seen_at DESC,id DESC LIMIT 256", serverID)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var n InterfaceRecord
		var raw, seen string
		if err = rows.Scan(&n.ID, &raw, &n.Present, &seen); err != nil {
			return v, err
		}
		if err = json.Unmarshal([]byte(raw), &n.Interface); err != nil {
			return v, err
		}
		n.LastSeenAt = parseTime(seen)
		v.Interfaces = append(v.Interfaces, n)
	}
	return v, rows.Err()
}

func (s *Store) InterfaceServer(ctx context.Context, interfaceID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT server_id FROM network_interfaces WHERE id=?", interfaceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}
