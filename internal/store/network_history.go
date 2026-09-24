package store

import (
	"context"
	"encoding/json"
	"errors"
)

type InterfacePage struct {
	Items   []InterfaceRecord `json:"items"`
	HasMore bool              `json:"has_more"`
	NextID  int64             `json:"next_id"`
}

// Stable keyset pagination includes retired identities without deleting their
// replay-protection baseline or historical ledger. Archiving is presentation only.
func (s *Store) InterfaceHistory(ctx context.Context, serverID, before int64, archived bool, limit int) (InterfacePage, error) {
	p := InterfacePage{Items: []InterfaceRecord{}}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,snapshot,present,last_seen_at,archived FROM network_interfaces WHERE server_id=? AND archived=? AND (?=0 OR id<?) ORDER BY id DESC LIMIT ?`, serverID, archived, before, before, limit+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var n InterfaceRecord
		var raw, seen string
		if err = rows.Scan(&n.ID, &raw, &n.Present, &seen, &n.Archived); err != nil {
			return p, err
		}
		if len(p.Items) == limit {
			p.HasMore = true
			break
		}
		if err = json.Unmarshal([]byte(raw), &n.Interface); err != nil {
			return p, err
		}
		n.LastSeenAt = parseTime(seen)
		p.Items = append(p.Items, n)
		p.NextID = n.ID
	}
	return p, rows.Err()
}
func (s *Store) ArchiveInterface(ctx context.Context, serverID, id int64, archived bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE network_interfaces SET archived=? WHERE server_id=? AND id=? AND (present=0 OR ?=0)`, archived, serverID, id, archived)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("接口仍在使用或记录不存在，不能归档")
	}
	return nil
}
