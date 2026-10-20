package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/natconfig"
)

const serverCols = `id, name, region, public_host, tags, notes, quota_bytes, quota_reset_day, quota_billing, core_mode, ipv4_only, cert_mode, enabled, created_at, updated_at, is_nat`

func scanServer(sc interface{ Scan(...any) error }) (domain.Server, error) {
	var v domain.Server
	var tags, created, updated string
	var ipv4Only, enabled, isNAT int
	if err := sc.Scan(&v.ID, &v.Name, &v.Region, &v.PublicHost, &tags, &v.Notes, &v.QuotaBytes, &v.QuotaResetDay, &v.QuotaBilling,
		&v.CoreMode, &ipv4Only, &v.CertMode, &enabled, &created, &updated, &isNAT); err != nil {
		return v, err
	}
	v.Tags = jsonList[string](tags)
	v.IsNAT = isNAT == 1
	v.IPv4Only = ipv4Only == 1
	v.Enabled = enabled == 1
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, nil
}

// CreateServer inserts a server and its (pending) agent record.
func (s *Store) CreateServer(ctx context.Context, v *domain.Server) error {
	return s.CreateServerWithCalibration(ctx, v, nil, time.Time{})
}

// CreateServerWithCalibration stores an optional provider reading for this
// billing period. New servers have no observed traffic at creation time.
func (s *Store) CreateServerWithCalibration(ctx context.Context, v *domain.Server, calibratedBytes *int64, periodStart time.Time, providerDirections ...[2]int64) error {
	return s.CreateServerWithNATAndCalibration(ctx, v, nil, calibratedBytes, periodStart, providerDirections...)
}

// CreateServerWithNATAndCalibration atomically stores onboarding intent before agent enrollment.
func (s *Store) CreateServerWithNATAndCalibration(ctx context.Context, v *domain.Server, config *natconfig.Config, calibratedBytes *int64, periodStart time.Time, providerDirections ...[2]int64) error {
	c, err := normalizeNAT(config)
	if err != nil {
		return err
	}
	if c != nil {
		if !v.IsNAT || len(c.Mappings) == 0 {
			return errors.New("只有 NAT 服务器可以录入非空端口映射")
		}
		if err := validateNATPublicHost(v.PublicHost); err != nil {
			return fmt.Errorf("%w: %v", ErrNATPublicHost, err)
		}
	}
	if v.QuotaBytes < 0 {
		return errors.New("invalid server quota")
	}
	now := s.Now()
	if v.CoreMode == "" {
		v.CoreMode = domain.CoreModeStable
	}
	mode, ok := domain.NormalizeServerQuotaBilling(v.QuotaBilling)
	if !ok {
		return errors.New("invalid server quota billing mode")
	}
	v.QuotaBilling = mode
	if v.CertMode == "" {
		v.CertMode = "self_signed"
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO servers(name,region,public_host,tags,notes,quota_bytes,quota_reset_day,quota_billing,core_mode,ipv4_only,cert_mode,enabled,created_at,updated_at,is_nat)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			v.Name, v.Region, v.PublicHost, jsonStr(v.Tags), v.Notes, v.QuotaBytes, v.QuotaResetDay, v.QuotaBilling, v.CoreMode, b2i(v.IPv4Only), v.CertMode, b2i(v.Enabled), fmtTime(now), fmtTime(now), b2i(v.IsNAT))
		if err != nil {
			return err
		}
		v.ID, _ = res.LastInsertId()
		v.CreatedAt, v.UpdatedAt = now, now
		if _, err = tx.ExecContext(ctx, `INSERT INTO agents(server_id,created_at,updated_at) VALUES (?,?,?)`, v.ID, fmtTime(now), fmtTime(now)); err != nil {
			return err
		}
		if c != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO server_nat_configs(server_id,revision,config,updated_at) VALUES(?,1,?,?)`, v.ID, jsonStr(c), fmtTime(now)); err != nil {
				return err
			}
		}
		return saveServerUsageCalibrationTx(ctx, tx, v.ID, v.QuotaBilling, calibratedBytes, periodStart, now, providerDirections...)
	})
}

// UpdateServer saves editable fields.
func (s *Store) UpdateServer(ctx context.Context, v *domain.Server) error {
	return s.UpdateServerWithCalibration(ctx, v, nil, time.Time{})
}

// UpdateServerWithCalibration preserves the current calibration on ordinary
// edits and clears it if the billing reset day or quota mode changes.
func (s *Store) UpdateServerWithCalibration(ctx context.Context, v *domain.Server, calibratedBytes *int64, periodStart time.Time, providerDirections ...[2]int64) error {
	if v.QuotaBytes < 0 {
		return errors.New("invalid server quota")
	}
	now := s.Now()
	mode, ok := domain.NormalizeServerQuotaBilling(v.QuotaBilling)
	if !ok {
		return errors.New("invalid server quota billing mode")
	}
	v.QuotaBilling = mode
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		vnat, err := s.serverNAT(ctx, tx, v.ID)
		if err != nil {
			return err
		}
		if (vnat.DesiredConfig != nil && len(vnat.DesiredConfig.Mappings) > 0) || (vnat.Report != nil && vnat.Report.Configured) {
			if err := validateNATPublicHost(v.PublicHost); err != nil {
				return fmt.Errorf("%w: %v", ErrNATPublicHost, err)
			}
		}
		var oldResetDay, oldNAT int
		var oldBilling string
		if err := tx.QueryRowContext(ctx, `SELECT quota_reset_day, quota_billing, is_nat FROM servers WHERE id=?`, v.ID).Scan(&oldResetDay, &oldBilling, &oldNAT); err != nil {
			return err
		}
		if oldNAT == 1 && !v.IsNAT && ((vnat.DesiredConfig != nil && len(vnat.DesiredConfig.Mappings) > 0) || (vnat.Report != nil && (vnat.Report.Configured || vnat.Report.Status == "error" || vnat.Report.Status == "pending")) || vnat.Pending) {
			return fmt.Errorf("%w: 请先清除端口映射并等待生效，再改为独立公网", ErrNATResources)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE servers SET name=?, region=?, public_host=?, tags=?, notes=?, quota_bytes=?, quota_reset_day=?, quota_billing=?, core_mode=?, ipv4_only=?, cert_mode=?, enabled=?, updated_at=?, is_nat=? WHERE id=?`,
			v.Name, v.Region, v.PublicHost, jsonStr(v.Tags), v.Notes, v.QuotaBytes, v.QuotaResetDay, v.QuotaBilling, v.CoreMode, b2i(v.IPv4Only), v.CertMode, b2i(v.Enabled), fmtTime(now), b2i(v.IsNAT), v.ID); err != nil {
			return err
		}
		if oldResetDay != v.QuotaResetDay || oldBilling != v.QuotaBilling {
			if _, err := tx.ExecContext(ctx, `DELETE FROM server_usage_calibrations WHERE server_id=?`, v.ID); err != nil {
				return err
			}
		}
		if err := saveServerUsageCalibrationTx(ctx, tx, v.ID, v.QuotaBilling, calibratedBytes, periodStart, now, providerDirections...); err != nil {
			return err
		}
		v.UpdatedAt = now
		return nil
	})
}

// DeleteServer atomically removes the server, its nodes and dependent chains.
func (s *Store) DeleteServer(ctx context.Context, id int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error { return deleteServerTx(ctx, tx, id) })
}

func deleteServerTx(ctx context.Context, tx *sql.Tx, id int64) error {
	if err := requireNoAccessRoutes(ctx, tx, id, true); err != nil {
		return err
	}
	// Prefer the recorded landing identity, which survives endpoint changes.
	// Older chains without an origin still need the copied endpoint fallback.
	// Resolve all dependencies before foreign keys clear the landing identity.
	if _, err := tx.ExecContext(ctx, `WITH RECURSIVE removed(id) AS (
			SELECT id FROM nodes WHERE server_id=?
			UNION
			SELECT chain.id FROM nodes chain JOIN nodes landing
			ON chain.server=landing.server AND chain.port=landing.port AND chain.protocol=landing.protocol
			LEFT JOIN node_chain_origins origin ON origin.chain_id=chain.id
			WHERE chain.source='chain' AND landing.server_id=?
			AND origin.landing_id IS NULL
			UNION
			SELECT chain.id FROM nodes chain JOIN removed ON chain.chain_front_node_id=removed.id
			WHERE chain.source='chain'
			UNION
			SELECT origin.chain_id FROM node_chain_origins origin JOIN removed ON origin.landing_id=removed.id
		) DELETE FROM nodes WHERE id IN (SELECT id FROM removed)`, id, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM servers WHERE id=?`, id)
	return err
}

// GetServer fetches one server.
func (s *Store) GetServer(ctx context.Context, id int64) (domain.Server, error) {
	v, err := scanServer(s.db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, id))
	if isNoRows(err) {
		return v, ErrNotFound
	}
	return v, err
}

// ListServers returns all servers.
func (s *Store) ListServers(ctx context.Context) ([]domain.Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverCols+` FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Server{}
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- agents ----

const agentCols = `id, server_id, token_hash, enroll_token_hash, enroll_expires_at, version, last_seen_at, applied_revision, applied_hash, apply_error, public_ipv4, public_ipv6, metrics, diagnostics, created_at, updated_at`

func scanAgent(sc interface{ Scan(...any) error }) (domain.Agent, error) {
	var a domain.Agent
	var enrollExp, lastSeen sql.NullString
	var metrics, diag, created, updated string
	if err := sc.Scan(&a.ID, &a.ServerID, &a.TokenHash, &a.EnrollTokenHash, &enrollExp, &a.Version, &lastSeen, &a.AppliedRevision, &a.AppliedHash, &a.ApplyError,
		&a.PublicIPv4, &a.PublicIPv6, &metrics, &diag, &created, &updated); err != nil {
		return a, err
	}
	a.EnrollExpiresAt = parseTimePtr(enrollExp)
	a.LastSeenAt = parseTimePtr(lastSeen)
	a.Metrics = rawOrEmpty(metrics)
	a.Diagnostics = rawOrEmpty(diag)
	a.CreatedAt = parseTime(created)
	a.UpdatedAt = parseTime(updated)
	return a, nil
}

// GetAgentByServer returns the agent record of a server.
func (s *Store) GetAgentByServer(ctx context.Context, serverID int64) (domain.Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE server_id=?`, serverID))
	if isNoRows(err) {
		return a, ErrNotFound
	}
	return a, err
}

// GetAgentByTokenHash resolves an authenticated agent.
func (s *Store) GetAgentByTokenHash(ctx context.Context, hash string) (domain.Agent, error) {
	if hash == "" {
		return domain.Agent{}, ErrNotFound
	}
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE token_hash=?`, hash))
	if isNoRows(err) {
		return a, ErrNotFound
	}
	return a, err
}

// GetAgentByEnrollHash resolves a pending enrolment token.
func (s *Store) GetAgentByEnrollHash(ctx context.Context, hash string) (domain.Agent, error) {
	if hash == "" {
		return domain.Agent{}, ErrNotFound
	}
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE enroll_token_hash=?`, hash))
	if isNoRows(err) {
		return a, ErrNotFound
	}
	return a, err
}

// ListAgents returns all agents.
func (s *Store) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agents ORDER BY server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAgentEnrollToken stores a new one-time enrolment token hash.
func (s *Store) SetAgentEnrollToken(ctx context.Context, serverID int64, hash string, expiresAt any) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET enroll_token_hash=?, enroll_expires_at=?, updated_at=? WHERE server_id=?`,
		hash, expiresAt, fmtTime(s.Now()), serverID)
	return err
}

// CompleteEnrollment swaps the enrolment token for a permanent token.
func (s *Store) CompleteEnrollment(ctx context.Context, agentID int64, enrollHash, tokenHash, version string) error {
	now := fmtTime(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET token_hash=?, enroll_token_hash='', enroll_expires_at=NULL, version=?, last_seen_at=?, updated_at=? WHERE id=? AND enroll_token_hash=? AND enroll_token_hash!='' AND enroll_expires_at>?`,
		tokenHash, version, now, now, agentID, enrollHash, now)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

// ResetAgentToken revokes the current token (agent must re-enrol).
func (s *Store) ResetAgentToken(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET token_hash='', updated_at=? WHERE server_id=?`, fmtTime(s.Now()), serverID)
	return err
}

// Heartbeat updates liveness, metrics and public addresses.
func (s *Store) Heartbeat(ctx context.Context, agentID int64, version, ipv4, ipv6 string, metrics, diagnostics []byte) error {
	now := fmtTime(s.Now())
	if len(metrics) == 0 {
		metrics = []byte("{}")
	}
	if len(diagnostics) == 0 {
		diagnostics = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET version=?, last_seen_at=?, public_ipv4=?, public_ipv6=?, metrics=?, diagnostics=?, updated_at=? WHERE id=?`,
		version, now, ipv4, ipv6, string(metrics), string(diagnostics), now, agentID)
	return err
}

// SetAgentApplied records the revision the agent reports as applied.
func (s *Store) SetAgentApplied(ctx context.Context, agentID, revision int64, hash, applyErr string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET applied_revision=?, applied_hash=?, apply_error=?, updated_at=? WHERE id=?`,
		revision, hash, applyErr, fmtTime(s.Now()), agentID)
	return err
}

// AgentConnlogSeq returns the last accepted connection-log sequence.
func (s *Store) AgentConnlogSeq(ctx context.Context, agentID int64) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT connlog_seq FROM agents WHERE id=?`, agentID).Scan(&seq)
	return seq, err
}

// SetAgentConnlogSeq stores the last accepted connection-log sequence.
func (s *Store) SetAgentConnlogSeq(ctx context.Context, agentID, seq int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET connlog_seq=? WHERE id=?`, seq, agentID)
	return err
}
