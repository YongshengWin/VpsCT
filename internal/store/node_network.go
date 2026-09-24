package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

// NetworkBindingVersion remains set after detach/delete. A future explicit
// downgrade workflow must confirm local cleanup before removing this contract.
func (s *Store) NetworkBindingVersion(ctx context.Context, serverID int64) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT binding_version FROM server_network_requirements WHERE server_id=?`, serverID).Scan(&version)
	if isNoRows(err) {
		return 0, nil
	}
	return version, err
}

// Like binding_version, this requirement survives the final detach. A direct-
// only agent cannot safely apply cleanup for a previously running transport.
func (s *Store) NetworkWireGuardVersion(ctx context.Context, serverID int64) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT wireguard_version FROM server_network_requirements WHERE server_id=?`, serverID).Scan(&v)
	if isNoRows(err) {
		return 0, nil
	}
	return v, err
}

func (s *Store) NetworkSSHVersion(ctx context.Context, serverID int64) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT ssh_version FROM server_network_requirements WHERE server_id=?`, serverID).Scan(&version)
	if isNoRows(err) {
		return 0, nil
	}
	return version, err
}

func (s *Store) NetworkEgressVersion(ctx context.Context, serverID int64) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT egress_version FROM server_network_requirements WHERE server_id=?`, serverID).Scan(&version)
	if isNoRows(err) {
		return 0, nil
	}
	return version, err
}

func (s *Store) SetNodeNetwork(ctx context.Context, nodeID, expected int64, policy *networkconfig.Node, advertiseHost *string) (domain.Node, error) {
	var out domain.Node
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = s.setNodeNetwork(ctx, tx, nodeID, expected, policy, advertiseHost)
		return err
	})
	// A concurrent SQLite read/write snapshot is an optimistic edit conflict.
	if err != nil && (strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked")) {
		err = ErrNetworkConflict
	}
	return out, err
}

func (s *Store) setNodeNetwork(ctx context.Context, q querier, nodeID, expected int64, policy *networkconfig.Node, advertiseHost *string) (domain.Node, error) {
	var empty domain.Node
	if expected < 0 || expected >= 1<<53-1 {
		return empty, ErrNetworkConflict
	}
	if policy != nil {
		if err := policy.Validate(); err != nil {
			return empty, err
		}
	}
	n, err := s.scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, nodeID))
	if isNoRows(err) {
		err = ErrNotFound
	}
	if err != nil {
		return empty, err
	}
	if n.Source != domain.NodeDeployed || n.ServerID == nil {
		return empty, errors.New("只有受管部署节点可以设置服务器网络")
	}
	if n.NetworkRevision != expected {
		return empty, ErrNetworkConflict
	}
	var managed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_transits WHERE entry_node_id=? AND stage<>'retired')`, nodeID).Scan(&managed); err != nil {
		return empty, err
	}
	if managed {
		return empty, ErrManagedTransit
	}
	if policy != nil {
		if n.Core != domain.CoreSingBox && policy.EgressProfileID != 0 {
			return empty, ErrNetworkCoreVersion
		}
		if !domain.ProtocolAllowed(n.Protocol, domain.CoreModeStable) || n.Core != domain.CoreFor(n.Protocol, domain.CoreModeStable) {
			return empty, ErrNetworkCoreVersion
		}
		if err := s.checkBindingCoreVersion(ctx, q, n.Core); err != nil {
			return empty, err
		}
	}
	n.Server, err = s.nodeNetworkHost(ctx, q, n, policy, advertiseHost)
	if err != nil {
		return empty, err
	}
	var profileID, profileRevision any
	if policy != nil && policy.EgressProfileID != 0 {
		p, err := scanEgress(q.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=? AND server_id=?`, policy.EgressProfileID, *n.ServerID))
		if err != nil {
			return empty, err
		}
		if !p.Enabled && (n.Network == nil || n.Network.EgressProfileID != policy.EgressProfileID || n.Network.EgressRevision != policy.EgressRevision) {
			return empty, errors.New("不能新增对已停用出口的绑定")
		}
		var found int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM egress_profile_revisions WHERE profile_id=? AND revision=? AND server_id=?`, p.ID, policy.EgressRevision, *n.ServerID).Scan(&found); err != nil {
			if isNoRows(err) {
				return empty, ErrNotFound
			}
			return empty, err
		}
		if p.Kind == "wireguard" {
			if err := s.claimWireGuardIdentity(ctx, q, *n.ServerID, n.ID, p.ID, policy.EgressRevision, true); err != nil {
				return empty, err
			}
		}
		if p.Kind == "ss2022" {
			if err := s.checkSS2022Core(ctx, q); err != nil {
				return empty, err
			}
		}
		profileID, profileRevision = policy.EgressProfileID, policy.EgressRevision
	}
	now := fmtTime(s.Now())
	res, err := q.ExecContext(ctx, `INSERT INTO node_networks(node_id,server_id,revision,policy,egress_profile_id,egress_revision,updated_at)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET revision=excluded.revision,policy=excluded.policy,egress_profile_id=excluded.egress_profile_id,egress_revision=excluded.egress_revision,updated_at=excluded.updated_at WHERE node_networks.revision=?`, nodeID, *n.ServerID, expected+1, jsonStr(policy), profileID, profileRevision, now, expected)
	if err != nil {
		return empty, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return empty, err
	}
	if count != 1 {
		return empty, ErrNetworkConflict
	}
	if _, err = q.ExecContext(ctx, `UPDATE nodes SET server=?,updated_at=? WHERE id=?`, n.Server, now, nodeID); err != nil {
		return empty, err
	}
	return s.scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, nodeID))
}

func (s *Store) nodeNetworkHost(ctx context.Context, q querier, n domain.Node, policy *networkconfig.Node, advertiseHost *string) (string, error) {
	if advertiseHost != nil {
		if policy == nil || policy.AdvertiseMode != "override" {
			return "", errors.New("仅独立访问地址模式可以指定地址")
		}
		n.Server = strings.TrimSpace(*advertiseHost)
	}
	if policy != nil && policy.AdvertiseMode == "override" {
		if err := networkconfig.ValidateAdvertiseHost(n.Server); err != nil {
			return "", err
		}
	}
	if policy == nil || policy.AdvertiseMode == "inherit" {
		var inherited string
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(public_host,''),(SELECT COALESCE(NULLIF(public_ipv4,''),NULLIF(public_ipv6,'')) FROM agents WHERE server_id=servers.id),'') FROM servers WHERE id=?`, *n.ServerID).Scan(&inherited); err != nil {
			return "", err
		}
		if inherited != "" {
			n.Server = inherited
		}
	}
	return n.Server, nil
}

// UpdateInheritedNodeHosts resolves inheritance in the UPDATE statement so a
// stale heartbeat cannot overwrite a concurrently saved per-node override.
func (s *Store) UpdateInheritedNodeHosts(ctx context.Context, serverID int64, host string) error {
	if host == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET server=?,updated_at=? WHERE server_id=? AND source='deployed' AND server<>? AND NOT EXISTS(SELECT 1 FROM node_networks WHERE node_id=nodes.id AND json_extract(policy,'$.advertise_mode')='override')`, host, fmtTime(s.Now()), serverID, host)
	return err
}
