package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"ctlvps/internal/domain"
)

const nodeCols = `id, name, protocol, server, port, params, server_params, source, server_id, listen_port, core, share_id, external_sub_id, chain_front_node_id, enabled, owner_user_id, tags, sort_order, revoked, created_at, updated_at`

func (s *Store) scanNode(sc interface{ Scan(...any) error }) (domain.Node, error) {
	var n domain.Node
	var params, serverParams, tags, created, updated string
	var serverID, shareID, extID, chainID sql.NullInt64
	var enabled, revoked int
	if err := sc.Scan(&n.ID, &n.Name, &n.Protocol, &n.Server, &n.Port, s.scanSecret("nodes.params", &params), s.scanSecret("nodes.server_params", &serverParams), &n.Source, &serverID, &n.ListenPort, &n.Core,
		&shareID, &extID, &chainID, &enabled, &n.OwnerUserID, &tags, &n.SortOrder, &revoked, &created, &updated); err != nil {
		return n, err
	}
	n.Params = rawOrEmpty(params)
	n.ServerParams = rawOrEmpty(serverParams)
	n.ServerID = intPtr(serverID)
	n.ShareID = intPtr(shareID)
	n.ExternalSubID = intPtr(extID)
	n.ChainFrontNodeID = intPtr(chainID)
	n.Enabled = enabled == 1
	n.Revoked = revoked == 1
	n.Tags = jsonList[string](tags)
	n.CreatedAt = parseTime(created)
	n.UpdatedAt = parseTime(updated)
	return n, nil
}

func (s *Store) nodeArgs(n *domain.Node) []any {
	if n.Tags == nil {
		n.Tags = []string{}
	}
	if len(n.Params) == 0 {
		n.Params = []byte("{}")
	}
	if len(n.ServerParams) == 0 {
		n.ServerParams = []byte("{}")
	}
	if n.Source == "" {
		n.Source = domain.NodeManual
	}
	return []any{n.Name, n.Protocol, n.Server, n.Port, s.seal("nodes.params", string(n.Params)), s.seal("nodes.server_params", string(n.ServerParams)), n.Source, nullInt(n.ServerID), n.ListenPort, n.Core,
		nullInt(n.ShareID), nullInt(n.ExternalSubID), nullInt(n.ChainFrontNodeID), b2i(n.Enabled), n.OwnerUserID, jsonStr(n.Tags), n.SortOrder, b2i(n.Revoked)}
}

// CreateNode inserts a node.
func (s *Store) CreateNode(ctx context.Context, n *domain.Node) error {
	return s.createNode(ctx, s.db, n)
}

func (s *Store) createNode(ctx context.Context, q querier, n *domain.Node) error {
	now := s.Now()
	args := append(s.nodeArgs(n), fmtTime(now), fmtTime(now))
	res, err := q.ExecContext(ctx, `INSERT INTO nodes(name,protocol,server,port,params,server_params,source,server_id,listen_port,core,share_id,external_sub_id,chain_front_node_id,enabled,owner_user_id,tags,sort_order,revoked,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	if err != nil {
		return err
	}
	n.ID, _ = res.LastInsertId()
	n.CreatedAt, n.UpdatedAt = now, now
	return nil
}

// UpdateNode saves all editable fields.
func (s *Store) UpdateNode(ctx context.Context, n *domain.Node) error {
	now := s.Now()
	args := append(s.nodeArgs(n), fmtTime(now), n.ID)
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET name=?,protocol=?,server=?,port=?,params=?,server_params=?,source=?,server_id=?,listen_port=?,core=?,share_id=?,external_sub_id=?,chain_front_node_id=?,enabled=?,owner_user_id=?,tags=?,sort_order=?,revoked=?,updated_at=? WHERE id=?`, args...)
	n.UpdatedAt = now
	return err
}

// DeleteNode removes a node.
func (s *Store) DeleteNode(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id)
	return err
}

// GetNode fetches one node.
func (s *Store) GetNode(ctx context.Context, id int64) (domain.Node, error) {
	n, err := s.scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, id))
	if isNoRows(err) {
		return n, ErrNotFound
	}
	return n, err
}

// NodeFilter narrows ListNodes.
type NodeFilter struct {
	Source         domain.NodeSource
	ServerID       *int64
	ExternalSubID  *int64
	ShareID        *int64
	IDs            []int64
	OnlyEnabled    bool
	IncludeRevoked bool
}

// ListNodes returns nodes matching f ordered by sort_order, id.
func (s *Store) ListNodes(ctx context.Context, f NodeFilter) ([]domain.Node, error) {
	var where []string
	var args []any
	if f.Source != "" {
		where = append(where, "source=?")
		args = append(args, f.Source)
	}
	if f.ServerID != nil {
		where = append(where, "server_id=?")
		args = append(args, *f.ServerID)
	}
	if f.ExternalSubID != nil {
		where = append(where, "external_sub_id=?")
		args = append(args, *f.ExternalSubID)
	}
	if f.ShareID != nil {
		where = append(where, "share_id=?")
		args = append(args, *f.ShareID)
	}
	if f.OnlyEnabled {
		where = append(where, "enabled=1")
	}
	if !f.IncludeRevoked {
		where = append(where, "revoked=0")
	}
	if len(f.IDs) > 0 {
		ph := make([]string, len(f.IDs))
		for i, id := range f.IDs {
			ph[i] = "?"
			args = append(args, id)
		}
		where = append(where, fmt.Sprintf("id IN (%s)", strings.Join(ph, ",")))
	}
	q := `SELECT ` + nodeCols + ` FROM nodes`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY sort_order, id"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Node{}
	for rows.Next() {
		n, err := s.scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ReplaceExternalNodes atomically swaps the imported node set of one
// external subscription, preserving ids of nodes whose name is unchanged so
// that generated subscriptions referencing them keep working.
func (s *Store) ReplaceExternalNodes(ctx context.Context, extID int64, fresh []domain.Node) (added, updated, removed int, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE external_sub_id=?`, extID)
		if err != nil {
			return err
		}
		existing := map[string]domain.Node{}
		for rows.Next() {
			n, err := s.scanNode(rows)
			if err != nil {
				rows.Close()
				return err
			}
			existing[n.Name] = n
		}
		rows.Close()
		now := fmtTime(s.Now())
		seen := map[string]bool{}
		for i := range fresh {
			n := &fresh[i]
			n.ExternalSubID = &extID
			n.Source = domain.NodeImported
			if seen[n.Name] {
				continue // duplicate names within a feed: keep first
			}
			seen[n.Name] = true
			if old, ok := existing[n.Name]; ok {
				n.ID = old.ID
				n.Tags = old.Tags
				n.SortOrder = old.SortOrder
				n.Enabled = old.Enabled
				n.OwnerUserID = old.OwnerUserID
				args := append(s.nodeArgs(n), now, n.ID)
				if _, err := tx.ExecContext(ctx, `UPDATE nodes SET name=?,protocol=?,server=?,port=?,params=?,server_params=?,source=?,server_id=?,listen_port=?,core=?,share_id=?,external_sub_id=?,chain_front_node_id=?,enabled=?,owner_user_id=?,tags=?,sort_order=?,revoked=?,updated_at=? WHERE id=?`, args...); err != nil {
					return err
				}
				updated++
				continue
			}
			n.Enabled = true
			n.SortOrder = i
			if err := s.createNode(ctx, tx, n); err != nil {
				return err
			}
			added++
		}
		for name, old := range existing {
			if !seen[name] {
				if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, old.ID); err != nil {
					return err
				}
				removed++
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE external_subscriptions SET node_count=? WHERE id=?`, len(seen), extID)
		return err
	})
	return
}

// UsedListenPorts returns ports already allocated on a server.
func (s *Store) UsedListenPorts(ctx context.Context, serverID int64) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT listen_port FROM nodes WHERE server_id=? AND listen_port>0`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// ReorderNodes applies a new sort order for the given ids.
func (s *Store) ReorderNodes(ctx context.Context, ids []int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE nodes SET sort_order=? WHERE id=?`, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// FindChainNode returns the virtual chain node for a front + landing endpoint.
func (s *Store) FindChainNode(ctx context.Context, frontID int64, server string, port int) (domain.Node, error) {
	n, err := s.scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE source=? AND chain_front_node_id=? AND server=? AND port=? AND revoked=0`,
		domain.NodeChain, frontID, server, port))
	if isNoRows(err) {
		return n, ErrNotFound
	}
	return n, err
}

// SplitInlineChains turns leftover "landing.chain_front = front" marks into
// standalone chain nodes so the original landing stays a normal node.
func (s *Store) SplitInlineChains(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE source<>? AND chain_front_node_id IS NOT NULL AND chain_front_node_id<>0`, domain.NodeChain)
	if err != nil {
		if strings.Contains(err.Error(), "no such column") {
			return nil
		}
		return err
	}
	defer rows.Close()
	var marked []domain.Node
	for rows.Next() {
		n, err := s.scanNode(rows)
		if err != nil {
			return err
		}
		marked = append(marked, n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, landing := range marked {
		if landing.ChainFrontNodeID == nil {
			continue
		}
		front, err := s.GetNode(ctx, *landing.ChainFrontNodeID)
		if err == nil {
			if _, err := s.FindChainNode(ctx, front.ID, landing.Server, landing.Port); err != nil {
				ch := domain.Node{
					Name:             front.Name + " → " + landing.Name,
					Protocol:         landing.Protocol,
					Server:           landing.Server,
					Port:             landing.Port,
					Params:           landing.Params,
					Source:           domain.NodeChain,
					ChainFrontNodeID: landing.ChainFrontNodeID,
					Enabled:          landing.Enabled,
					OwnerUserID:      landing.OwnerUserID,
					Tags:             []string{},
				}
				if err := s.CreateNode(ctx, &ch); err != nil {
					return err
				}
			}
		}
		landing.ChainFrontNodeID = nil
		if err := s.UpdateNode(ctx, &landing); err != nil {
			return err
		}
	}
	return nil
}

// UpdateNodeCredentials also refreshes virtual copies of this landing endpoint.
// Keep identity, ordering and enabled/revoked state unchanged.
func (s *Store) UpdateNodeCredentials(ctx context.Context, n *domain.Node) error {
	now := fmtTime(s.Now())
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE nodes SET params=?,server_params=?,core=?,updated_at=? WHERE id=?`,
			s.seal("nodes.params", string(n.Params)), s.seal("nodes.server_params", string(n.ServerParams)), n.Core, now, n.ID)
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `UPDATE nodes SET params=?,updated_at=? WHERE source=? AND server=? AND port=? AND protocol=?`,
			s.seal("nodes.params", string(n.Params)), now, domain.NodeChain, n.Server, n.Port, n.Protocol)
		return err
	})
}
