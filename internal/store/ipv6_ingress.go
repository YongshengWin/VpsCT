package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
)

func (s *Store) IPv6IngressVersion(ctx context.Context, serverID int64) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT ipv6_ingress_version FROM server_network_requirements WHERE server_id=?`, serverID).Scan(&v)
	if isNoRows(err) {
		return 0, nil
	}
	return v, err
}

type IPv6DeploymentPreview struct {
	Host              string           `json:"advertise_host"`
	ListenInterfaceID string           `json:"listen_interface_id,omitempty"`
	ListenAddress     string           `json:"listen_address"`
	Readiness         NetworkReadiness `json:"readiness"`
}

func (s *Store) PreviewIPv6Deployment(ctx context.Context, n domain.Node) (IPv6DeploymentPreview, error) {
	var preview IPv6DeploymentPreview
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		preview, err = s.previewIPv6Deployment(ctx, tx, n)
		return err
	})
	return preview, err
}

func (s *Store) previewIPv6Deployment(ctx context.Context, q querier, n domain.Node) (IPv6DeploymentPreview, error) {
	var preview IPv6DeploymentPreview
	if n.ServerID == nil || n.Core != domain.CoreSingBox || n.Network == nil || n.Network.IngressFamily != "ipv6" {
		return preview, errors.New("仅 IPv6 部署需要受管 sing-box 节点和 IPv6 网络策略")
	}
	state, err := s.networkReadinessForCore(ctx, q, *n.ServerID, n.Core)
	if err != nil {
		return preview, err
	}
	if state.server.IsNAT || !state.server.Enabled {
		return preview, errors.New("仅 IPv6 部署需要已启用的独立公网服务器")
	}
	preview.Readiness, err = s.previewNodeNetwork(ctx, q, n, n.Network)
	if err != nil {
		return preview, err
	}
	preview.Host, err = s.nodeNetworkHost(ctx, q, n, n.Network, nil)
	if err != nil {
		return preview, err
	}
	preview.Readiness.Ready = true
	checks := preview.Readiness.Checks[:0]
	for _, check := range preview.Readiness.Checks {
		// The first core can be installed by the first desired revision.
		if check.Code == "installed_core" && preview.Readiness.InstalledCoreVersion == "" {
			continue
		}
		checks = append(checks, check)
		preview.Readiness.Ready = preview.Readiness.Ready && check.Ready
	}
	preview.Readiness.Checks = checks
	if !preview.Readiness.Ready {
		return preview, nil
	}
	preview.ListenInterfaceID = n.Network.ListenInterfaceID
	if address, err := netip.ParseAddr(n.Network.ListenAddress); err == nil {
		preview.ListenAddress = address.String()
	}
	if n.Network.ListenMode == "public_ipv6" {
		candidate, err := netinventory.PublicIPv6Candidate(state.inventory, n.Network.ListenInterfaceID)
		if err != nil {
			return preview, err
		}
		preview.ListenAddress, preview.ListenInterfaceID = candidate.Address, candidate.InterfaceID
	}
	return preview, nil
}

func (s *Store) priorIPv6Deployment(ctx context.Context, q querier, serverID, ownerID int64, operationID, fingerprint string) (domain.Node, bool, error) {
	var id sql.NullInt64
	var owner int64
	var recorded string
	err := q.QueryRowContext(ctx, `SELECT owner_user_id,fingerprint,node_id FROM ipv6_deployments WHERE server_id=? AND operation_id=?`, serverID, operationID).Scan(&owner, &recorded, &id)
	if isNoRows(err) {
		return domain.Node{}, false, nil
	}
	if err != nil {
		return domain.Node{}, false, err
	}
	if owner != ownerID || recorded != fingerprint || !id.Valid {
		return domain.Node{}, false, ErrNetworkConflict
	}
	node, err := s.scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, id.Int64))
	return node, true, err
}

func (s *Store) PriorIPv6Deployment(ctx context.Context, serverID, ownerID int64, operationID, fingerprint string) (domain.Node, bool, error) {
	return s.priorIPv6Deployment(ctx, s.db, serverID, ownerID, operationID, fingerprint)
}

// CreateIPv6Deployment persists the listener policy and idempotency record
// with the node, before any desired state can contain that node.
func (s *Store) CreateIPv6Deployment(ctx context.Context, n *domain.Node, operationID, fingerprint string, checked IPv6DeploymentPreview, audit domain.AuditEvent) error {
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		prior, exists, err := s.priorIPv6Deployment(ctx, tx, *n.ServerID, n.OwnerUserID, operationID, fingerprint)
		if err != nil {
			return err
		}
		if exists {
			*n = prior
			return nil
		}
		current, err := s.previewIPv6Deployment(ctx, tx, *n)
		if err != nil {
			return err
		}
		if !current.Readiness.Ready || current.Host != checked.Host || current.ListenAddress != checked.ListenAddress || current.ListenInterfaceID != checked.ListenInterfaceID || current.Readiness.PinnedCoreVersion != checked.Readiness.PinnedCoreVersion {
			return ErrNetworkConflict
		}
		used, err := s.usedListenPorts(ctx, tx, *n.ServerID)
		if err != nil {
			return err
		}
		if used[n.ListenPort] {
			return ErrNetworkConflict
		}
		if err := s.createNode(ctx, tx, n); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ipv6_deployments(server_id,operation_id,owner_user_id,fingerprint,node_id) VALUES(?,?,?,?,?)`, *n.ServerID, operationID, n.OwnerUserID, fingerprint, n.ID)
		if err != nil {
			return err
		}
		if err := s.recordIPv6Endpoint(ctx, tx, IPv6EndpointCheck{NodeID: n.ID, Host: current.Host, ListenIPv6: current.ListenAddress}, "ok", ""); err != nil {
			return err
		}
		return s.addAudit(ctx, tx, audit)
	})
	if err != nil && (strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked")) {
		return ErrNetworkConflict
	}
	return err
}

func (s *Store) HasUnpublishedIPv6Deployments(ctx context.Context, serverID int64) (bool, error) {
	var pending bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
        SELECT 1 FROM ipv6_deployments d JOIN nodes n ON n.id=d.node_id JOIN servers sv ON sv.id=d.server_id
        WHERE d.server_id=? AND sv.enabled=1 AND n.enabled=1 AND n.revoked=0
          AND NOT EXISTS(SELECT 1 FROM node_application_candidates c WHERE c.node_id=n.id
            AND c.server_id=d.server_id AND c.revision=(SELECT MAX(revision) FROM desired_states WHERE server_id=d.server_id))
        )`, serverID).Scan(&pending)
	return pending, err
}

type IPv6EndpointCheck struct {
	NodeID     int64
	Host       string
	ListenIPv6 string
}

func (s *Store) IPv6EndpointChecksDue(ctx context.Context, limit int) ([]IPv6EndpointCheck, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n.server,a.listen_ipv6 FROM nodes n
        JOIN node_networks nn ON nn.node_id=n.id
        JOIN node_applied_states a ON a.node_id=n.id AND a.server_id=n.server_id
        LEFT JOIN ipv6_endpoint_checks c ON c.node_id=n.id AND c.host=n.server AND c.listen_address=a.listen_ipv6
        WHERE n.enabled=1 AND n.revoked=0 AND a.status='applied' AND a.listen_ipv6<>''
          AND json_extract(nn.policy,'$.ingress_family')='ipv6'
          AND (c.checked_at IS NULL OR c.checked_at<?)
        GROUP BY n.server,a.listen_ipv6
        ORDER BY MIN(COALESCE(c.checked_at,'')),n.server,a.listen_ipv6 LIMIT ?`, fmtTime(s.Now().Add(-time.Minute)), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []IPv6EndpointCheck
	for rows.Next() {
		var check IPv6EndpointCheck
		if err := rows.Scan(&check.Host, &check.ListenIPv6); err != nil {
			return nil, err
		}
		due = append(due, check)
	}
	return due, rows.Err()
}

func (s *Store) RecordIPv6EndpointCheck(ctx context.Context, check IPv6EndpointCheck, state, message string) error {
	now := fmtTime(s.Now())
	lastGood := ""
	if state == "ok" {
		lastGood = now
	}
	_, err := s.db.ExecContext(ctx, ipv6CheckInsert+`
        SELECT n.id,n.server,a.listen_ipv6,?,?,?,? FROM nodes n
        JOIN node_applied_states a ON a.node_id=n.id AND a.server_id=n.server_id
        WHERE n.server=? AND a.listen_ipv6=? AND a.status='applied'
        `+ipv6CheckUpdate, state, message, now, lastGood, check.Host, check.ListenIPv6)
	return err
}

const ipv6CheckInsert = `INSERT INTO ipv6_endpoint_checks(node_id,host,listen_address,state,message,checked_at,last_good_at)`
const ipv6CheckUpdate = `ON CONFLICT(node_id) DO UPDATE SET host=excluded.host,listen_address=excluded.listen_address,
        state=excluded.state,message=excluded.message,checked_at=excluded.checked_at,last_good_at=CASE
        WHEN excluded.state='temporary' AND ipv6_endpoint_checks.host=excluded.host
          AND ipv6_endpoint_checks.listen_address=excluded.listen_address THEN ipv6_endpoint_checks.last_good_at
        ELSE excluded.last_good_at END`

func (s *Store) recordIPv6Endpoint(ctx context.Context, q querier, check IPv6EndpointCheck, state, message string) error {
	now := fmtTime(s.Now())
	lastGood := ""
	if state == "ok" {
		lastGood = now
	}
	_, err := q.ExecContext(ctx, ipv6CheckInsert+` VALUES(?,?,?,?,?,?,?) `+ipv6CheckUpdate,
		check.NodeID, check.Host, check.ListenIPv6, state, message, now, lastGood)
	return err
}

func (s *Store) ipv6NodeAvailable(ctx context.Context, q querier, n domain.Node, listenIPv6 string) (bool, error) {
	if !networkconfig.IsPublicIPv6(listenIPv6) {
		return false, nil
	}
	var raw, diagnostics string
	err := q.QueryRowContext(ctx, `SELECT sn.snapshot,ag.diagnostics FROM agents ag
        JOIN network_snapshots sn ON sn.server_id=ag.server_id WHERE ag.server_id=?`, *n.ServerID).Scan(&raw, &diagnostics)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var diag agentproto.Diagnostics
	var inventory agentproto.NetworkSnapshot
	if json.Unmarshal([]byte(diagnostics), &diag) != nil || diag.NetworkBindingErrors[n.ID] != "" || diag.NetworkGuardError != "" ||
		json.Unmarshal([]byte(raw), &inventory) != nil {
		return false, nil
	}
	policy := *n.Network
	policy.EgressProfileID, policy.EgressRevision = 0, 0
	if netinventory.ValidateBindingSelection(policy, nil, &inventory, false) != nil {
		return false, nil
	}
	if policy.ListenMode == "public_ipv6" {
		candidate, err := netinventory.PublicIPv6Candidate(&inventory, policy.ListenInterfaceID)
		if err != nil || candidate.Address != listenIPv6 {
			return false, nil
		}
	}
	if ip, err := netip.ParseAddr(n.Server); err == nil {
		return ip.String() == listenIPv6, nil
	}
	var state, lastGood string
	err = q.QueryRowContext(ctx, `SELECT state,last_good_at FROM ipv6_endpoint_checks
        WHERE node_id=? AND host=? AND listen_address=?`, n.ID, n.Server, listenIPv6).Scan(&state, &lastGood)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return state != "invalid" && s.Now().Sub(parseTime(lastGood)) <= 5*time.Minute, nil
}
