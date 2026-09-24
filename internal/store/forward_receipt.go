package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"ctlvps/internal/agentproto"
)

// CheckForwardReceipt runs inside the accounting transaction. A replay must
// be byte-equivalent even after its retired resources have been removed.
func (s *Store) CheckForwardReceipt(ctx context.Context, tx *sql.Tx, serverID int64, r agentproto.ForwardReceipt) (bool, []agentproto.ForwardSpec, error) {
	if err := r.Validate(); err != nil {
		return false, nil, err
	}
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT digest FROM forward_receipts WHERE server_id=? AND id=?`, serverID, r.ID).Scan(&digest)
	if err == nil {
		if digest != r.Digest() {
			return false, nil, errors.New("转发回执编号内容冲突")
		}
		return true, nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	var raw, hash string
	if err := tx.QueryRowContext(ctx, `SELECT payload,hash FROM desired_states WHERE server_id=? AND revision=?`, serverID, r.Revision).Scan(s.scanSecret("desired_states.payload", &raw), &hash); err != nil {
		return false, nil, err
	}
	var ds agentproto.DesiredState
	if hash != r.Hash || json.Unmarshal([]byte(raw), &ds) != nil || ds.NetworkForwardVersion != agentproto.NetworkForwardVersion || len(ds.Forwards) != len(r.Counters) {
		return false, nil, errors.New("转发回执与已发布配置不一致")
	}
	byID := map[int64]bool{}
	for _, c := range r.Counters {
		byID[c.ForwardID] = true
	}
	for _, f := range ds.Forwards {
		if !byID[f.ForwardID] {
			return false, nil, errors.New("转发回执缺少计量身份")
		}
		var revision, owner int64
		if err := tx.QueryRowContext(ctx, `SELECT server_id,revision FROM port_forwards WHERE id=?`, f.ForwardID).Scan(&owner, &revision); err != nil {
			return false, nil, err
		}
		if owner != serverID || revision < f.Revision {
			return false, nil, errors.New("转发回执使用了其他服务器或未来版本")
		}
	}
	return false, ds.Forwards, nil
}

func CommitForwardReceipt(ctx context.Context, tx *sql.Tx, serverID int64, r agentproto.ForwardReceipt, applied []agentproto.ForwardSpec, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO forward_receipts VALUES(?,?,?,?,?)`, serverID, r.ID, r.Digest(), r.Revision, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	// Complete the matching operation before retirement advances generation.
	if _, err := tx.ExecContext(ctx, `UPDATE network_operations SET status='applied',message='配置及转发清理已确认',updated_at=? WHERE server_id=? AND desired_revision=? AND desired_hash=? AND status IN ('waiting_agent','apply_failed')`, networkOperationTime(now), serverID, r.Revision, r.Hash); err != nil {
		return err
	}
	for _, f := range applied {
		if f.Retired {
			result, err := tx.ExecContext(ctx, `DELETE FROM port_forwards WHERE id=? AND server_id=? AND revision=? AND retired=1`, f.ForwardID, serverID, f.Revision)
			if err != nil {
				return err
			}
			if count, _ := result.RowsAffected(); count != 1 {
				return errors.New("转发退役版本不匹配")
			}
			continue
		}
		// A delayed receipt cannot free any port from its applied revision or
		// a newer queued/published revision. It only retires earlier listeners.
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_listener_reservations WHERE server_id=? AND resource_kind='forward' AND resource_id=? AND listen_port NOT IN (SELECT json_extract(config,'$.listen_port') FROM port_forward_revisions WHERE forward_id=? AND revision>=?)`, serverID, f.ForwardID, f.ForwardID, f.Revision); err != nil {
			return err
		}
	}
	return nil
}
