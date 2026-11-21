package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"ctlvps/internal/domain"
)

func (s *Store) ControllerOwner(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM controller_owner WHERE singleton=1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) TransferControllerOwner(ctx context.Context, actor domain.User, targetID int64, audit domain.AuditEvent) error {
	if actor.ID <= 0 || targetID <= 0 || actor.ID == targetID {
		return ErrPermissionDenied
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE controller_owner SET user_id=? WHERE singleton=1 AND user_id=?
 AND EXISTS (SELECT 1 FROM users WHERE id=? AND role='admin' AND enabled=1 AND security_version=?)
 AND EXISTS (SELECT 1 FROM users WHERE id=? AND role='admin' AND enabled=1)`, targetID, actor.ID, actor.ID, actor.SecurityVersion, targetID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return ErrPermissionDenied
		}
		audit.Action, audit.Target = "owner.transfer", fmt.Sprintf("user:%d", targetID)
		audit.Detail, _ = json.Marshal(map[string]any{"previous_owner_id": actor.ID, "new_owner_id": targetID})
		return s.addAudit(ctx, tx, audit)
	})
}
