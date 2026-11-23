package store

import (
	"context"
	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
)

// Bindings outlive deselection. Re-selecting an ID cannot reset its accounting
// or reuse an old identity for a different combination. Legacy rows stay intact.
const shareEntrySchema = `CREATE TABLE share_entry_bindings (
 node_id INTEGER PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 share_id INTEGER NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
 server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 entry_id TEXT NOT NULL, snapshot TEXT NOT NULL,
 UNIQUE(share_id,server_id,entry_id)
);
DROP TRIGGER network_generation_share_node_usage;
CREATE TRIGGER network_generation_share_node_usage AFTER UPDATE OF node_usage ON shares
WHEN NEW.node_usage IS NOT OLD.node_usage BEGIN
 UPDATE server_network_generations SET generation=generation+1 WHERE server_id IN (
 WITH limits(server_id,node_id,quota) AS (
 SELECT n.server_id,n.id,q.value FROM nodes n,json_each(NEW.targets) t,json_each(t.value,'$.quota_bytes') q
 WHERE n.share_id=NEW.id AND n.server_id=json_extract(t.value,'$.server_id') AND n.protocol=q.key AND q.value>0 AND NOT EXISTS(SELECT 1 FROM share_entry_bindings b WHERE b.node_id=n.id)
 UNION ALL
 SELECT json_extract(t.value,'$.server_id'),json_extract(e.value,'$.node_id'),json_extract(e.value,'$.quota_bytes') FROM json_each(NEW.targets) t,json_each(t.value,'$.entries') e WHERE json_extract(e.value,'$.quota_bytes')>0
 ), totals AS (SELECT *,coalesce(json_extract(OLD.node_usage,'$."'||node_id||'"[0]'),0) old_up,coalesce(json_extract(OLD.node_usage,'$."'||node_id||'"[1]'),0) old_down,coalesce(json_extract(NEW.node_usage,'$."'||node_id||'"[0]'),0) new_up,coalesce(json_extract(NEW.node_usage,'$."'||node_id||'"[1]'),0) new_down FROM limits)
 SELECT server_id FROM totals WHERE ((CASE OLD.billing_mode WHEN 'up' THEN old_up WHEN 'down' THEN old_down WHEN 'max' THEN max(old_up,old_down) ELSE old_up+old_down END)>=quota) IS NOT ((CASE NEW.billing_mode WHEN 'up' THEN new_up WHEN 'down' THEN new_down WHEN 'max' THEN max(new_up,new_down) ELSE new_up+new_down END)>=quota)
 ); END;`

type ShareEntryBinding struct {
	ID       string
	Snapshot domain.ShareEntryConfig
}

func (s *Store) ShareEntryBindings(ctx context.Context, id int64) (map[int64]ShareEntryBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id,entry_id,snapshot FROM share_entry_bindings WHERE share_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ShareEntryBinding{}
	for rows.Next() {
		var e ShareEntryBinding
		var node int64
		var raw []byte
		if err := rows.Scan(&node, &e.ID, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &e.Snapshot); err != nil {
			return nil, err
		}
		out[node] = e
	}
	return out, rows.Err()
}

var ErrShareChanged = errors.New("分享配置或状态已改变，请刷新后重试")
var ErrShareSnapshot = errors.New("已有组合入口 ID 的配置快照不能更改，请使用新的 ID")

// A reconciliation plan may be prepared outside the transaction. Fence it by
// the selection/status it read, not counters that can legitimately change.
func checkShareSelection(ctx context.Context, tx *sql.Tx, sh domain.Share) error {
	var targets string
	var status domain.ShareStatus
	if err := tx.QueryRowContext(ctx, `SELECT targets,status FROM shares WHERE id=?`, sh.ID).Scan(&targets, &status); err != nil {
		return err
	}
	var current []domain.ShareTarget
	if err := json.Unmarshal([]byte(targets), &current); err != nil {
		return err
	}
	if status != sh.Status || !reflect.DeepEqual(current, sh.Targets) {
		return ErrShareChanged
	}
	return nil
}

// CommitShareEntries assigns store-owned node IDs before persisting the target
// snapshot. A caller can never attach an existing foreign node via request JSON.
func (s *Store) CommitShareEntries(ctx context.Context, prev domain.Share, sh *domain.Share, saveSettings bool, creates, updates []domain.Node, entries map[int]*domain.ShareEntry, rotateToken string) error {
	now := fmtTime(s.Now())
	return s.Tx(ctx, func(tx *sql.Tx) error {
		// Acquire the SQLite writer lock before reading the selection. A concurrent
		// meter commit must not invalidate a deferred read snapshot mid-upgrade.
		if _, err := tx.ExecContext(ctx, `UPDATE shares SET id=id WHERE 0`); err != nil {
			return err
		}
		if err := checkShareSelection(ctx, tx, prev); err != nil {
			return err
		}
		for i := range creates {
			n := &creates[i]
			if err := s.createShareNode(ctx, tx, n); err != nil {
				return err
			}
			if e := entries[i]; e != nil {
				if _, err := tx.ExecContext(ctx, `INSERT INTO share_entry_bindings(node_id,share_id,server_id,entry_id,snapshot) VALUES(?,?,?,?,?)`, n.ID, sh.ID, *n.ServerID, e.ID, jsonStr(e.Snapshot)); err != nil {
					return err
				}
				e.NodeID = n.ID
			}
		}
		for i := range updates {
			if err := s.updateNode(ctx, tx, &updates[i]); err != nil {
				return err
			}
		}
		if saveSettings {
			if err := s.updateShareSettings(ctx, tx, sh); err != nil {
				return err
			}
			owner := int64(0)
			allowed := []int64{}
			if sh.UserID != nil {
				owner = *sh.UserID
				allowed = append(allowed, owner)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE subscriptions SET name=?,template_id=?,proxy_groups='[]',rules='[]',owner_user_id=?,allowed_user_ids=?,updated_at=? WHERE share_id=?`, sh.Name, nullInt(sh.TemplateID), owner, jsonStr(allowed), now, sh.ID); err != nil {
				return err
			}
		} else if !reflect.DeepEqual(sh.Targets, prev.Targets) {
			if _, err := tx.ExecContext(ctx, `UPDATE shares SET targets=? WHERE id=?`, jsonStr(sh.Targets), sh.ID); err != nil {
				return err
			}
		}
		if rotateToken != "" {
			var shortHash string
			if err := tx.QueryRowContext(ctx, `SELECT short_code_hash FROM subscriptions WHERE share_id=?`, sh.ID).Scan(&shortHash); err != nil {
				return err
			}
			short := ""
			if shortHash != "" && shortHash != auth.HashToken("") {
				short = auth.NewSubscriptionToken()
			}
			if _, err := tx.ExecContext(ctx, `UPDATE subscriptions SET token=?,token_hash=?,token_hint=?,short_code=?,short_code_hash=?,enabled=1,updated_at=? WHERE share_id=?`, s.seal("subscriptions.token", rotateToken), auth.HashToken(rotateToken), auth.TokenHint(rotateToken), s.seal("subscriptions.short_code", short), auth.HashToken(short), fmtTime(s.Now()), sh.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, activateShareSQL, now, now, sh.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) RevokeShareEntries(ctx context.Context, id int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{`UPDATE nodes SET revoked=1,enabled=0,updated_at=? WHERE share_id=?`, `UPDATE subscriptions SET enabled=0,updated_at=? WHERE share_id=?`, `UPDATE shares SET status='revoked',updated_at=? WHERE id=?`} {
			if _, err := tx.ExecContext(ctx, q, fmtTime(s.Now()), id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Automatic quota/expiry gates must be applied before an access-granting commit.
const activateShareSQL = `UPDATE shares SET status=CASE WHEN expires_at IS NOT NULL AND julianday(expires_at)<=julianday(?) THEN 'expired' WHEN quota_bytes>0 AND (CASE billing_mode WHEN 'up' THEN used_upload WHEN 'down' THEN used_download WHEN 'max' THEN max(used_upload,used_download) ELSE used_upload+used_download END)>=quota_bytes THEN 'exhausted' ELSE 'active' END,updated_at=? WHERE id=?`

func (s *Store) ResumeShare(ctx context.Context, id int64) error {
	now := fmtTime(s.Now())
	r, err := s.db.ExecContext(ctx, activateShareSQL+` AND status<>'revoked'`, now, now, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ErrShareChanged
	}
	return err
}
func (s *Store) SetShareConnlog(ctx context.Context, id int64, on bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shares SET connlog_enabled=?,updated_at=? WHERE id=?`, b2i(on), fmtTime(s.Now()), id)
	return err
}
