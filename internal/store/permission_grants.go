package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"ctlvps/internal/authorization"
	"ctlvps/internal/domain"
)

var ErrPermissionDenied = errors.New("permission delegation denied")
var ErrPermissionCycle = errors.New("permission delegation cycle")

// permissionSnapshot reads principals and all grants through the same database
// transaction so no partial delegation chain can authorize a write.
func permissionSnapshot(ctx context.Context, q querier) (authorization.Evaluator, error) {
	e := authorization.Evaluator{Principals: map[int64]authorization.Principal{}}
	rows, err := q.QueryContext(ctx, `SELECT id,enabled,role FROM users`)
	if err != nil {
		return e, err
	}
	for rows.Next() {
		var p authorization.Principal
		var enabled int
		var role domain.Role
		if err := rows.Scan(&p.ID, &enabled, &role); err != nil {
			rows.Close()
			return e, err
		}
		p.Enabled, p.Admin = enabled != 0, role == domain.RoleAdmin
		e.Principals[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return e, err
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT id,user_id,granted_by,permission,scope FROM permission_grants ORDER BY id`)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var g authorization.Grant
		var scope string
		if err := rows.Scan(&g.ID, &g.UserID, &g.GrantedBy, &g.Permission, &scope); err != nil {
			return e, err
		}
		if err := json.Unmarshal([]byte(scope), &g.Scope); err != nil {
			return e, err
		}
		e.Grants = append(e.Grants, g)
	}
	return e, rows.Err()
}

func (s *Store) PermissionSnapshot(ctx context.Context) (authorization.Evaluator, error) {
	var result authorization.Evaluator
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = permissionSnapshot(ctx, tx)
		return err
	})
	return result, err
}

// SavePermissionGrant verifies issuer authority, scope existence, and graph
// acyclicity in the same transaction as the grant and audit. It invalidates
// affected account credentials so long-lived sessions cannot retain access.
func (s *Store) SavePermissionGrant(ctx context.Context, actor domain.User, g *authorization.Grant, audit domain.AuditEvent) error {
	if actor.ID <= 0 || g.UserID <= 0 || actor.ID == g.UserID || !authorization.ValidGrantScope(g.Permission, g.Scope) {
		return ErrPermissionDenied
	}
	scope, err := authorization.ValidateScope(g.Scope)
	if err != nil {
		return ErrPermissionDenied
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		// Acquire a write lock before loading the graph to serialize privilege
		// checks with role, grant and account changes on other connections.
		res, err := tx.ExecContext(ctx, `UPDATE users SET security_version=security_version WHERE id=? AND enabled=1 AND security_version=?`, actor.ID, actor.SecurityVersion)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return ErrPermissionDenied
		}
		e, err := permissionSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		if target, ok := e.Principals[g.UserID]; !ok || target.Admin {
			return ErrPermissionDenied
		}
		if !e.Allows(actor.ID, "accounts.edit", authorization.Scope{Kind: "all"}) || !e.Allows(actor.ID, authorization.Delegate, authorization.Scope{Kind: "all"}) || !e.Allows(actor.ID, g.Permission, scope) {
			return ErrPermissionDenied
		}
		var cycle int
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (
 SELECT user_id FROM permission_grants WHERE granted_by=?
 UNION SELECT p.user_id FROM permission_grants p JOIN descendants d ON p.granted_by=d.id
 ) SELECT EXISTS(SELECT 1 FROM descendants WHERE id=?)`, g.UserID, actor.ID).Scan(&cycle); err != nil {
			return err
		}
		if cycle != 0 {
			return ErrPermissionCycle
		}
		table := ""
		if scope.Kind == "servers" {
			table = "servers"
		} else if scope.Kind == "shares" {
			table = "shares"
		}
		for _, id := range scope.IDs {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=?)`, id).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNotFound
			}
		}
		raw, err := json.Marshal(scope)
		if err != nil {
			return err
		}
		now := fmtTime(s.Now())
		if err := tx.QueryRowContext(ctx, `INSERT INTO permission_grants(user_id,granted_by,permission,scope,created_at,updated_at)
 VALUES(?,?,?,?,?,?) ON CONFLICT(user_id,granted_by,permission) DO UPDATE SET scope=excluded.scope,updated_at=excluded.updated_at RETURNING id`,
			g.UserID, actor.ID, g.Permission, string(raw), now, now).Scan(&g.ID); err != nil {
			return err
		}
		g.GrantedBy, g.Scope = actor.ID, scope
		if err := invalidatePermissionDescendants(ctx, tx, g.UserID); err != nil {
			return err
		}
		audit.Action, audit.Target = "permission.grant", fmt.Sprintf("user:%d", g.UserID)
		audit.Detail, _ = json.Marshal(map[string]any{"permission": g.Permission, "scope": scope, "grant_id": g.ID})
		return s.addAudit(ctx, tx, audit)
	})
}

func invalidatePermissionDescendants(ctx context.Context, q querier, userID int64) error {
	_, err := q.ExecContext(ctx, `WITH RECURSIVE affected(id) AS (
 SELECT ? UNION SELECT p.user_id FROM permission_grants p JOIN affected a ON p.granted_by=a.id
 ) UPDATE users SET security_version=security_version+1 WHERE id IN (SELECT id FROM affected)`, userID)
	return err
}

// RevokePermissionGrant allows the issuer or an active full administrator to
// remove a grant. An issuer can always retract their own grant after losing
// delegation authority; revocation cannot increase access.
func (s *Store) RevokePermissionGrant(ctx context.Context, actor domain.User, id int64, audit domain.AuditEvent) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET security_version=security_version WHERE id=? AND enabled=1 AND security_version=?`, actor.ID, actor.SecurityVersion)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return ErrPermissionDenied
		}
		var targetID, issuerID int64
		var permission string
		if err := tx.QueryRowContext(ctx, `SELECT user_id,granted_by,permission FROM permission_grants WHERE id=?`, id).Scan(&targetID, &issuerID, &permission); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var role domain.Role
		if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id=?`, actor.ID).Scan(&role); err != nil {
			return err
		}
		if actor.ID != issuerID && role != domain.RoleAdmin {
			return ErrPermissionDenied
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM permission_grants WHERE id=?`, id); err != nil {
			return err
		}
		if err := invalidatePermissionDescendants(ctx, tx, targetID); err != nil {
			return err
		}
		audit.Action, audit.Target = "permission.revoke", fmt.Sprintf("user:%d", targetID)
		audit.Detail, _ = json.Marshal(map[string]any{"permission": permission, "grant_id": id})
		return s.addAudit(ctx, tx, audit)
	})
}

// ReplaceIssuedPermissionGrants replaces only this issuer's grants to one
// account in a single transaction. Other administrators' grants are retained.
func (s *Store) ReplaceIssuedPermissionGrants(ctx context.Context, actor domain.User, targetID int64, grants []authorization.Grant, audit domain.AuditEvent) ([]authorization.Grant, error) {
	if actor.ID <= 0 || targetID <= 0 || actor.ID == targetID || len(grants) > 128 {
		return nil, ErrPermissionDenied
	}
	seen := map[string]bool{}
	for i := range grants {
		g := &grants[i]
		if seen[g.Permission] || !authorization.ValidGrantScope(g.Permission, g.Scope) {
			return nil, ErrPermissionDenied
		}
		seen[g.Permission] = true
		g.Scope, _ = authorization.ValidateScope(g.Scope)
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET security_version=security_version WHERE id=? AND enabled=1 AND security_version=?`, actor.ID, actor.SecurityVersion)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return ErrPermissionDenied
		}
		e, err := permissionSnapshot(ctx, tx)
		if err != nil {
			return err
		}
		if target, ok := e.Principals[targetID]; !ok || target.Admin {
			return ErrPermissionDenied
		}
		all := authorization.Scope{Kind: "all"}
		if !e.Allows(actor.ID, "accounts.edit", all) || !e.Allows(actor.ID, authorization.Delegate, all) {
			return ErrPermissionDenied
		}
		var cycle int
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (
 SELECT user_id FROM permission_grants WHERE granted_by=?
 UNION SELECT p.user_id FROM permission_grants p JOIN descendants d ON p.granted_by=d.id
 ) SELECT EXISTS(SELECT 1 FROM descendants WHERE id=?)`, targetID, actor.ID).Scan(&cycle); err != nil {
			return err
		}
		if cycle != 0 {
			return ErrPermissionCycle
		}
		for _, g := range grants {
			if !e.Allows(actor.ID, g.Permission, g.Scope) {
				return ErrPermissionDenied
			}
			table := ""
			if g.Scope.Kind == "servers" {
				table = "servers"
			} else if g.Scope.Kind == "shares" {
				table = "shares"
			}
			for _, id := range g.Scope.IDs {
				var exists int
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=?)`, id).Scan(&exists); err != nil {
					return err
				}
				if exists == 0 {
					return ErrNotFound
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM permission_grants WHERE user_id=? AND granted_by=?`, targetID, actor.ID); err != nil {
			return err
		}
		now := fmtTime(s.Now())
		for i := range grants {
			g := &grants[i]
			g.UserID, g.GrantedBy = targetID, actor.ID
			raw, err := json.Marshal(g.Scope)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO permission_grants(user_id,granted_by,permission,scope,created_at,updated_at) VALUES(?,?,?,?,?,?)`, targetID, actor.ID, g.Permission, string(raw), now, now)
			if err != nil {
				return err
			}
			g.ID, err = res.LastInsertId()
			if err != nil {
				return err
			}
		}
		if err := invalidatePermissionDescendants(ctx, tx, targetID); err != nil {
			return err
		}
		audit.Action, audit.Target = "permission.replace", fmt.Sprintf("user:%d", targetID)
		audit.Detail, _ = json.Marshal(map[string]any{"grants": grants})
		return s.addAudit(ctx, tx, audit)
	})
	if err != nil {
		return nil, err
	}
	return grants, nil
}
