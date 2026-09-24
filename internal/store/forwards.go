package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

const MaxPortForwards = agentbudget.ActiveForwards
const MaxForwardRevisions = 4096
const forwardCols = `id,server_id,name,config,enabled,revision,retired,created_at,updated_at`

var ErrListenPortConflict = errors.New("该监听端口已被节点、转发或待清理资源占用")
var ErrForwardCapacity = errors.New("该服务器的转发数量已达上限，请先完成停用和清理")
var ErrForwardRetired = errors.New("转发正在清理，不能重新启用或编辑")

func (s *Store) NetworkForwardVersion(ctx context.Context, serverID int64) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT forward_version FROM server_network_requirements WHERE server_id=?`, serverID).Scan(&version)
	if isNoRows(err) {
		return 0, nil
	}
	return version, err
}

func forwardStoreError(err error) error {
	if err != nil && strings.Contains(err.Error(), "listener port already reserved") {
		return ErrListenPortConflict
	}
	return err
}

func scanForward(sc interface{ Scan(...any) error }) (domain.PortForward, error) {
	var f domain.PortForward
	var config, created, updated string
	var enabled, retired int
	err := sc.Scan(&f.ID, &f.ServerID, &f.Name, &config, &enabled, &f.Revision, &retired, &created, &updated)
	if isNoRows(err) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	f.Config, err = networkconfig.DecodeForward([]byte(config))
	f.Enabled, f.Retired = enabled == 1, retired == 1
	f.CreatedAt, f.UpdatedAt = parseTime(created), parseTime(updated)
	return f, err
}

func (s *Store) GetPortForward(ctx context.Context, id int64) (domain.PortForward, error) {
	return scanForward(s.db.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, id))
}

func (s *Store) ListPortForwards(ctx context.Context, serverID int64) ([]domain.PortForward, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE server_id=? ORDER BY id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PortForward{}
	for rows.Next() {
		f, err := scanForward(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func validateForward(f domain.PortForward) (domain.PortForward, string, error) {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || len(f.Name) > 128 || strings.ContainsAny(f.Name, "\x00\r\n") || f.ServerID < 1 || f.ServerID > 1<<53-1 {
		return f, "", errors.New("转发名称或服务器无效")
	}
	b, err := json.Marshal(f.Config)
	if err != nil {
		return f, "", err
	}
	f.Config, err = networkconfig.DecodeForward(b)
	if err != nil {
		return f, "", err
	}
	b, err = json.Marshal(f.Config)
	return f, string(b), err
}

func checkForwardEgress(ctx context.Context, q querier, f domain.PortForward) error {
	if f.Config.EgressProfileID == 0 {
		return nil
	}
	var kind string
	err := q.QueryRowContext(ctx, `SELECT p.kind FROM egress_profiles p JOIN egress_profile_revisions r ON r.profile_id=p.id
 WHERE p.id=? AND p.server_id=? AND r.revision=? AND r.server_id=p.server_id`, f.Config.EgressProfileID, f.ServerID, f.Config.EgressRevision).Scan(&kind)
	if isNoRows(err) {
		return errors.New("转发出口版本不存在或不属于当前服务器")
	}
	if err != nil {
		return err
	}
	if kind != "direct" && kind != "socks5" && kind != "ssh" && kind != "wireguard" {
		return errors.New("固定转发暂不支持该出口类型")
	}
	return nil
}

// A forward transport keeps its executable contract after the listener is
// retired. Otherwise an old controller or signed downgrade could reinterpret
// its remembered network state before cleanup finishes.
func markForwardTransportRequirement(ctx context.Context, q querier, f domain.PortForward) error {
	if f.Config.EgressProfileID == 0 {
		return nil
	}
	var kind string
	if err := q.QueryRowContext(ctx, `SELECT kind FROM egress_profiles WHERE id=? AND server_id=?`, f.Config.EgressProfileID, f.ServerID).Scan(&kind); err != nil {
		return err
	}
	if kind != "socks5" && kind != "ssh" && kind != "wireguard" {
		return nil
	}
	ssh, wg := 0, 0
	if kind == "ssh" {
		ssh = 1
	}
	if kind == "wireguard" {
		wg = 1
	}
	_, err := q.ExecContext(ctx, `UPDATE server_network_requirements SET egress_version=1, ssh_version=max(ssh_version,?), wireguard_version=max(wireguard_version,?) WHERE server_id=?`, ssh, wg, f.ServerID)
	return err
}

func forwardEgressIDs(f domain.PortForward) (any, any) {
	if f.Config.EgressProfileID == 0 {
		return nil, nil
	}
	return f.Config.EgressProfileID, f.Config.EgressRevision
}

// These transactional primitives are shared by the forthcoming reviewed
// operation API. They do not claim that saving a row applied a listener.
func (s *Store) createPortForward(ctx context.Context, tx *sql.Tx, f domain.PortForward) (domain.PortForward, error) {
	f, config, err := validateForward(f)
	if err != nil {
		return f, err
	}
	if err := s.checkBindingCoreVersion(ctx, tx, domain.CoreSingBox); err != nil {
		return f, err
	}
	if err := checkForwardEgress(ctx, tx, f); err != nil {
		return f, err
	}
	profile, revision := forwardEgressIDs(f)
	now := s.Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO port_forwards(server_id,name,config,listen_port,egress_profile_id,egress_revision,enabled,revision,retired,created_at,updated_at)
 SELECT ?,?,?,?,?,?,?,1,0,?,? WHERE (SELECT count(*) FROM port_forwards WHERE server_id=?)<?`,
		f.ServerID, f.Name, config, f.Config.ListenPort, profile, revision, b2i(f.Enabled), fmtTime(now), fmtTime(now), f.ServerID, MaxPortForwards)
	if err != nil {
		return f, forwardStoreError(err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return f, err
	}
	if count == 0 {
		return f, ErrForwardCapacity
	}
	f.ID, err = res.LastInsertId()
	f.Revision, f.Retired, f.CreatedAt, f.UpdatedAt = 1, false, now, now
	if err != nil {
		return f, err
	}
	return f, markForwardTransportRequirement(ctx, tx, f)
}

func (s *Store) updatePortForward(ctx context.Context, tx *sql.Tx, id, expected int64, name string, enabled bool, config networkconfig.Forward) (domain.PortForward, error) {
	f, err := scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, id))
	if err != nil {
		return f, err
	}
	if f.Revision != expected {
		return f, ErrNetworkConflict
	}
	if f.Retired {
		return f, ErrForwardRetired
	}
	// Reserve one final version for retirement even at the editing limit.
	if expected >= MaxForwardRevisions-1 {
		return f, errors.New("转发版本已达编辑上限，仍可停用并删除")
	}
	f.Name, f.Enabled, f.Config = name, enabled, config
	if enabled {
		if err := s.checkBindingCoreVersion(ctx, tx, domain.CoreSingBox); err != nil {
			return f, err
		}
	}
	f, raw, err := validateForward(f)
	if err != nil {
		return f, err
	}
	if err := checkForwardEgress(ctx, tx, f); err != nil {
		return f, err
	}
	profile, revision := forwardEgressIDs(f)
	f.UpdatedAt = s.Now()
	res, err := tx.ExecContext(ctx, `UPDATE port_forwards SET name=?,config=?,listen_port=?,egress_profile_id=?,egress_revision=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=? AND retired=0`,
		f.Name, raw, config.ListenPort, profile, revision, b2i(enabled), fmtTime(f.UpdatedAt), id, expected)
	if err != nil {
		return f, forwardStoreError(err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return f, err
		}
		return f, ErrNetworkConflict
	}
	f.Revision++
	return f, markForwardTransportRequirement(ctx, tx, f)
}

func (s *Store) retirePortForward(ctx context.Context, tx *sql.Tx, id, expected int64) (domain.PortForward, error) {
	f, err := scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, id))
	if err != nil {
		return f, err
	}
	if f.Revision != expected {
		return f, ErrNetworkConflict
	}
	if f.Retired {
		return f, ErrForwardRetired
	}
	if expected >= MaxForwardRevisions {
		return f, fmt.Errorf("转发版本无效")
	}
	f.UpdatedAt = s.Now()
	_, err = tx.ExecContext(ctx, `UPDATE port_forwards SET retired=1,enabled=0,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, fmtTime(f.UpdatedAt), id, expected)
	f.Revision, f.Retired, f.Enabled = expected+1, true, false
	return f, err
}

func (s *Store) PortForwardRevision(ctx context.Context, id, revision int64) (domain.PortForward, error) {
	return scanForward(s.db.QueryRowContext(ctx, `SELECT p.id,p.server_id,r.name,r.config,r.enabled,r.revision,r.retired,p.created_at,r.created_at
 FROM port_forwards p JOIN port_forward_revisions r ON r.forward_id=p.id WHERE p.id=? AND r.revision=?`, id, revision))
}
