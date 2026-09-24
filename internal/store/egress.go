package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

var ErrNetworkConflict = errors.New("网络配置已被其他操作修改，请刷新后重试")
var ErrEgressInUse = errors.New("出口配置仍被节点或转发引用，不能删除")

const MaxEgressProfiles = 128
const MaxEgressRevisions = 4096
const egressCols = `id,server_id,name,kind,enabled,current_revision,created_at,updated_at,COALESCE((SELECT stage FROM managed_transits WHERE profile_id=egress_profiles.id),'')`

func validateEgress(name, kind string, config json.RawMessage) (string, json.RawMessage, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "\x00\r\n") {
		return "", nil, errors.New("出口名称无效")
	}
	var value any
	var err error
	switch kind {
	case "direct":
		value, err = networkconfig.DecodeDirect(config)
	case "socks5":
		value, err = networkconfig.DecodeSOCKS5(config)
	case "ss2022":
		value, err = networkconfig.DecodeSS2022(config)
	case "ssh":
		value, err = networkconfig.DecodeSSH(config)
	case "wireguard":
		value, err = networkconfig.DecodeWireGuard(config)
	default:
		return "", nil, errors.New("尚未开放该出口类型")
	}
	if err != nil {
		return "", nil, err
	}
	normalized, err := json.Marshal(value)
	return name, normalized, err
}

func scanEgress(sc interface{ Scan(...any) error }) (domain.EgressProfile, error) {
	var p domain.EgressProfile
	var enabled int
	var created, updated string
	err := sc.Scan(&p.ID, &p.ServerID, &p.Name, &p.Kind, &enabled, &p.CurrentRevision, &created, &updated, &p.ManagedStage)
	p.Enabled, p.CreatedAt, p.UpdatedAt = enabled == 1, parseTime(created), parseTime(updated)
	if isNoRows(err) {
		err = ErrNotFound
	}
	return p, err
}

func (s *Store) GetEgressProfile(ctx context.Context, id int64) (domain.EgressProfile, error) {
	return scanEgress(s.db.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=?`, id))
}

func (s *Store) ListEgressProfiles(ctx context.Context, serverID int64) ([]domain.EgressProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE server_id=? ORDER BY id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.EgressProfile{}
	for rows.Next() {
		p, err := scanEgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateEgressProfile(ctx context.Context, p *domain.EgressProfile, config json.RawMessage) error {
	var created domain.EgressProfile
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		created, err = s.createEgressProfile(ctx, tx, *p, config)
		return err
	})
	if err == nil {
		*p = created
	}
	return err
}

func (s *Store) createEgressProfile(ctx context.Context, tx *sql.Tx, p domain.EgressProfile, config json.RawMessage) (domain.EgressProfile, error) {
	return s.createEgressProfileWithCredentials(ctx, tx, p, config, nil)
}

func (s *Store) createEgressProfileWithCredentials(ctx context.Context, tx *sql.Tx, p domain.EgressProfile, config json.RawMessage, supplied *networkconfig.SOCKS5Credentials) (domain.EgressProfile, error) {
	name, config, err := validateEgress(p.Name, p.Kind, config)
	if err != nil {
		return p, err
	}
	secret, err := s.prepareEgressCredentials(ctx, tx, p.Kind, config, supplied, nil)
	if err != nil {
		return p, err
	}
	now := s.Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO egress_profiles(server_id,name,kind,enabled,current_revision,created_at,updated_at)
 SELECT ?,?,?,?,1,?,? WHERE (SELECT count(*) FROM egress_profiles WHERE server_id=?)<?`, p.ServerID, name, p.Kind, b2i(p.Enabled), fmtTime(now), fmtTime(now), p.ServerID, MaxEgressProfiles)
	if err != nil {
		return p, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return p, err
	}
	if n != 1 {
		return p, ErrEgressCapacity
	}
	p.ID, err = res.LastInsertId()
	if err != nil {
		return p, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO egress_profile_revisions(profile_id,server_id,revision,config,created_at) VALUES(?,?,1,?,?)`, p.ID, p.ServerID, string(config), fmtTime(now))
	if err == nil {
		err = s.saveEgressCredentials(ctx, tx, p.ServerID, p.ID, 1, secret)
	}
	if err == nil {
		p.Name, p.CurrentRevision, p.CreatedAt, p.UpdatedAt = name, 1, now, now
	}
	return p, err
}

// AppendEgressRevision compares the displayed revision before saving. The
// profile's latest revision is a default for new bindings, never a live alias.
func (s *Store) AppendEgressRevision(ctx context.Context, id, expected int64, name string, enabled bool, config json.RawMessage) (domain.EgressProfile, error) {
	var p domain.EgressProfile
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = s.appendEgressRevision(ctx, tx, id, expected, name, enabled, config)
		return err
	})
	return p, err
}

func (s *Store) appendEgressRevision(ctx context.Context, tx *sql.Tx, id, expected int64, name string, enabled bool, config json.RawMessage) (domain.EgressProfile, error) {
	return s.appendEgressRevisionWithCredentials(ctx, tx, id, expected, name, enabled, config, nil)
}

func (s *Store) appendEgressRevisionWithCredentials(ctx context.Context, tx *sql.Tx, id, expected int64, name string, enabled bool, config json.RawMessage, supplied *networkconfig.SOCKS5Credentials) (domain.EgressProfile, error) {
	if err := s.ManagedTransitProfile(ctx, tx, id); err != nil {
		return domain.EgressProfile{}, err
	}
	p, err := scanEgress(tx.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=?`, id))
	if err != nil {
		return p, err
	}
	if p.CurrentRevision != expected {
		return p, ErrNetworkConflict
	}
	name, config, err = validateEgress(name, p.Kind, config)
	if err != nil {
		return domain.EgressProfile{}, err
	}
	if expected < 1 || expected >= MaxEgressRevisions {
		return domain.EgressProfile{}, errors.New("出口版本无效或已达上限")
	}
	secret, err := s.prepareEgressCredentials(ctx, tx, p.Kind, config, supplied, &p)
	if err != nil {
		return p, err
	}
	now := fmtTime(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE egress_profiles SET name=?,enabled=?,current_revision=current_revision+1,updated_at=? WHERE id=? AND kind=? AND current_revision=?`, name, b2i(enabled), now, id, p.Kind, expected)
	if err != nil {
		return domain.EgressProfile{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.EgressProfile{}, err
	}
	if n != 1 {
		return domain.EgressProfile{}, ErrNetworkConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO egress_profile_revisions(profile_id,server_id,revision,config,created_at) SELECT id,server_id,current_revision,?,? FROM egress_profiles WHERE id=?`, string(config), now, id)
	if err != nil {
		return domain.EgressProfile{}, err
	}
	if err = s.saveEgressCredentials(ctx, tx, p.ServerID, p.ID, expected+1, secret); err != nil {
		return domain.EgressProfile{}, err
	}
	return scanEgress(tx.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=?`, id))
}

func (s *Store) GetEgressRevision(ctx context.Context, serverID, id, revision int64) (domain.EgressRevision, error) {
	var out domain.EgressRevision
	var config, created string
	err := s.db.QueryRowContext(ctx, `SELECT profile_id,server_id,revision,config,created_at,EXISTS(SELECT 1 FROM egress_profile_credentials c WHERE c.profile_id=r.profile_id AND c.server_id=r.server_id AND c.revision=r.revision) FROM egress_profile_revisions r WHERE server_id=? AND profile_id=? AND revision=?`, serverID, id, revision).Scan(&out.ProfileID, &out.ServerID, &out.Revision, &config, &created, &out.HasCredentials)
	if isNoRows(err) {
		return out, ErrNotFound
	}
	out.Config, out.CreatedAt = json.RawMessage(config), parseTime(created)
	return out, err
}

func (s *Store) DeleteEgressProfile(ctx context.Context, id, expected int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		return s.deleteEgressProfile(ctx, tx, id, expected)
	})
}

func (s *Store) deleteEgressProfile(ctx context.Context, tx *sql.Tx, id, expected int64) error {
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_transits WHERE profile_id=? AND stage<>'retired')`, id).Scan(&active); err != nil {
		return err
	}
	if active {
		return s.ManagedTransitProfile(ctx, tx, id)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM egress_profiles WHERE id=? AND current_revision=? AND NOT EXISTS(SELECT 1 FROM node_networks WHERE egress_profile_id=?) AND NOT EXISTS(SELECT 1 FROM port_forwards WHERE egress_profile_id=?)`, id, expected, id, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	p, err := scanEgress(tx.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=?`, id))
	if err != nil {
		return err
	}
	if p.CurrentRevision != expected {
		return ErrNetworkConflict
	}
	return ErrEgressInUse
}
