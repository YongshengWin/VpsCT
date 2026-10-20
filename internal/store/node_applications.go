package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

type nodeApplicationContext struct {
	PublicHost string          `json:"public_host"`
	CoreMode   domain.CoreMode `json:"core_mode"`
	IPv4Only   bool            `json:"ipv4_only"`
	CertMode   string          `json:"cert_mode"`
}

type nodeApplicationSnapshot struct {
	Node         domain.Node            `json:"node"`
	ServerParams json.RawMessage        `json:"server_params"`
	Context      nodeApplicationContext `json:"context"`
	Spec         agentproto.NodeSpec    `json:"spec"`
}

func applicationContext(s domain.Server, spec agentproto.NodeSpec) nodeApplicationContext {
	certMode := s.CertMode
	if spec.Cert == nil {
		certMode = ""
	}
	return nodeApplicationContext{s.PublicHost, s.CoreMode, s.IPv4Only, certMode}
}

func applicationLabel(serverID, nodeID, revision int64) string {
	return fmt.Sprintf("node_application:%d:%d:%d", serverID, nodeID, revision)
}

func equalApplicationJSON(a, b []byte) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

// captureNodeCandidates runs in the publication transaction, after its generation
// check. It binds the complete client projection to the immutable server spec.
func (s *Store) captureNodeCandidates(ctx context.Context, q querier, ds domain.DesiredState) error {
	var wanted agentproto.DesiredState
	if err := json.Unmarshal(ds.Payload, &wanted); err != nil {
		return err
	}
	sv, err := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, ds.ServerID))
	if err != nil {
		return err
	}
	for _, spec := range wanted.Nodes {
		n, err := s.scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=? AND server_id=? AND source='deployed'`, spec.NodeID, ds.ServerID))
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return err
		}
		snapshot := nodeApplicationSnapshot{Node: n, ServerParams: n.ServerParams, Context: applicationContext(sv, spec), Spec: spec}
		payload, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO node_application_candidates(server_id,revision,node_id,payload) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, ds.ServerID, ds.Revision, n.ID, s.seal(applicationLabel(ds.ServerID, n.ID, ds.Revision), string(payload))); err != nil {
			return err
		}
	}
	return nil
}

// BackfillNodeApplications preserves offline installations only when a stored
// exact successful receipt proves the historical configuration. Unconfirmed
// nodes remain pending. New client fields cannot be inferred from old versions.
func (s *Store) BackfillNodeApplications(ctx context.Context) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.backfillChainOrigins(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT d.`+"id,d.server_id,d.revision,d.payload,d.hash,d.status,d.error,d.created_at,d.applied_at"+` FROM desired_states d JOIN agents a ON a.server_id=d.server_id AND a.applied_revision=d.revision AND a.applied_hash=d.hash WHERE a.apply_error=''`)
		if err != nil {
			return err
		}
		var states []domain.DesiredState
		for rows.Next() {
			ds, err := s.scanDesired(rows)
			if err != nil {
				rows.Close()
				return err
			}
			states = append(states, ds)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, ds := range states {
			var wanted agentproto.DesiredState
			if err := json.Unmarshal(ds.Payload, &wanted); err != nil {
				return err
			}
			sv, err := scanServer(tx.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, ds.ServerID))
			if err != nil {
				return err
			}
			if sv.PublicHost != wanted.PublicHost || string(sv.CoreMode) != wanted.CoreMode || sv.IPv4Only != wanted.IPv4Only {
				continue
			}
			for _, spec := range wanted.Nodes {
				if spec.Blocked {
					continue
				}
				n, err := s.scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=? AND server_id=? AND source='deployed'`, spec.NodeID, ds.ServerID))
				if isNoRows(err) {
					continue
				}
				if err != nil {
					return err
				}
				params, _ := json.Marshal(spec.Params)
				if n.Protocol != spec.Protocol || n.ListenPort != spec.ListenPort || string(domain.CoreFor(n.Protocol, sv.CoreMode)) != spec.Core || !equalApplicationJSON(n.ServerParams, params) || !reflect.DeepEqual(n.ShareID, spec.ShareID) {
					continue
				}
				if (n.Network == nil) != (spec.Network == nil) || (n.Network != nil && !reflect.DeepEqual(*n.Network, spec.Network.Policy)) {
					continue
				}
				if spec.Cert != nil {
					var p map[string]any
					_ = json.Unmarshal(n.ServerParams, &p)
					mode, _ := p["cert_mode"].(string)
					if mode == "" {
						mode = sv.CertMode
					}
					if mode != spec.Cert.Mode {
						continue
					}
				}
				snapshot := nodeApplicationSnapshot{Node: n, ServerParams: n.ServerParams, Context: applicationContext(sv, spec), Spec: spec}
				payload, err := json.Marshal(snapshot)
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO node_applied_states(server_id,node_id,revision,hash,status,payload,updated_at) VALUES(?,?,?,?,'applied',?,?) ON CONFLICT DO NOTHING`, ds.ServerID, n.ID, ds.Revision, ds.Hash, s.seal(applicationLabel(ds.ServerID, n.ID, ds.Revision), string(payload)), fmtTime(s.Now()))
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// RecordNodeApplications accepts resource receipts only for a published hash and
// that server's own resources. Older replayed reports cannot undo a newer fence.
// Nil resources supports a successful old agent's whole-configuration receipt.
func (s *Store) RecordNodeApplications(ctx context.Context, serverID, revision int64, hash, status string, resources []agentproto.NodeApplication, sequence int64) error {
	if err := agentproto.ValidateNodeApplications(resources); err != nil {
		return err
	}
	if revision <= 0 || len(hash) != 64 {
		if len(resources) > 0 || status == "applied" {
			return errors.New("应用回执版本或摘要无效")
		}
		return nil
	}
	if sequence < 0 || sequence > 1<<53 {
		return errors.New("应用回执序号无效")
	}
	if len(resources) == 0 && status != "applied" {
		return nil
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		ds, err := s.scanDesired(tx.QueryRowContext(ctx, `SELECT `+desiredCols+` FROM desired_states WHERE server_id=? AND revision=? AND hash=?`, serverID, revision, hash))
		if isNoRows(err) {
			// A repeated heartbeat for a long-lived resource can outlive desired
			// history retention. Its encrypted applied snapshot remains durable.
			var known bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_applied_states WHERE server_id=? AND revision=? AND hash=?)`, serverID, revision, hash).Scan(&known); err != nil {
				return err
			}
			if known {
				return s.recordRetainedApplications(ctx, tx, serverID, revision, hash, resources, sequence)
			}
			return errors.New("应用回执未匹配已发布配置")
		}
		if err != nil {
			return err
		}
		var wanted agentproto.DesiredState
		if err := json.Unmarshal(ds.Payload, &wanted); err != nil {
			return err
		}
		byID := map[int64]agentproto.NodeSpec{}
		for _, n := range wanted.Nodes {
			byID[n.NodeID] = n
		}
		if len(resources) == 0 {
			if status != "applied" {
				return nil
			}
			for _, n := range wanted.Nodes {
				phase := "applied"
				if n.Blocked {
					phase = "blocked"
				}
				resources = append(resources, agentproto.NodeApplication{NodeID: n.NodeID, Core: n.Core, Status: phase})
			}
		}
		for _, receipt := range resources {
			n, ok := byID[receipt.NodeID]
			if !ok || n.Core != receipt.Core || (n.Blocked && receipt.Status == "applied") {
				return errors.New("节点应用回执与配置不符")
			}
			var payload string
			err := tx.QueryRowContext(ctx, `SELECT payload FROM node_application_candidates WHERE server_id=? AND revision=? AND node_id=?`, serverID, revision, n.NodeID).Scan(&payload)
			if isNoRows(err) {
				continue
			}
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO node_applied_states(server_id,node_id,revision,hash,status,payload,sequence,updated_at) SELECT ?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM nodes WHERE id=? AND server_id=?) ON CONFLICT(server_id,node_id) DO UPDATE SET revision=excluded.revision,hash=excluded.hash,status=excluded.status,payload=excluded.payload,sequence=excluded.sequence,updated_at=excluded.updated_at WHERE excluded.revision>node_applied_states.revision OR (excluded.revision=node_applied_states.revision AND (excluded.sequence>node_applied_states.sequence OR (excluded.sequence=node_applied_states.sequence AND excluded.status=node_applied_states.status)))`, serverID, n.NodeID, revision, hash, receipt.Status, payload, sequence, fmtTime(s.Now()), n.NodeID, serverID)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// The durable applied projection also authorizes later failure fences after
// desired-history pruning. Pruning must not freeze an old success forever.
func (s *Store) recordRetainedApplications(ctx context.Context, tx *sql.Tx, serverID, revision int64, hash string, resources []agentproto.NodeApplication, sequence int64) error {
	for _, receipt := range resources {
		var rowRevision int64
		var rowHash, payload string
		err := tx.QueryRowContext(ctx, `SELECT revision,hash,payload FROM node_applied_states WHERE server_id=? AND node_id=?`, serverID, receipt.NodeID).Scan(&rowRevision, &rowHash, &payload)
		if isNoRows(err) {
			return errors.New("节点应用回执未匹配保留配置")
		}
		if err != nil {
			return err
		}
		if rowRevision > revision {
			continue // A stale replay cannot overwrite a newer applied resource.
		}
		if rowRevision != revision || rowHash != hash {
			return errors.New("节点应用回执未匹配保留配置")
		}
		raw, err := s.decrypt(applicationLabel(serverID, receipt.NodeID, revision), payload)
		if err != nil {
			return err
		}
		var snapshot nodeApplicationSnapshot
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return err
		}
		if snapshot.Spec.Core != receipt.Core || (snapshot.Spec.Blocked && receipt.Status == "applied") {
			return errors.New("节点应用回执与保留配置不符")
		}
		_, err = tx.ExecContext(ctx, `UPDATE node_applied_states SET status=?,sequence=?,updated_at=? WHERE server_id=? AND node_id=? AND revision=? AND hash=? AND (sequence<? OR (sequence=? AND status=?))`, receipt.Status, sequence, fmtTime(s.Now()), serverID, receipt.NodeID, revision, hash, sequence, sequence, receipt.Status)
		if err != nil {
			return err
		}
	}
	return nil
}

// A failed desired revision is an attempt, never the actual applied revision.
// Only an exact published success may advance the agent's global boundary.
var ErrStaleApplication = errors.New("应用回执已被较新的资源状态替代")

func (s *Store) RecordAgentApplication(ctx context.Context, agentID, serverID, revision int64, hash, status, applyErr string, receiptSequence ...int64) error {
	if revision <= 0 {
		return nil
	}
	if status != "applied" && status != "failed" && status != "pending" {
		return nil
	}
	var sequence int64
	if len(receiptSequence) > 0 {
		sequence = receiptSequence[0]
	}
	if sequence < 0 || sequence > 1<<53 {
		return errors.New("应用回执序号无效")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var known bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM desired_states WHERE server_id=? AND revision=? AND hash=?) OR EXISTS(SELECT 1 FROM agents WHERE id=? AND server_id=? AND applied_revision=? AND applied_hash=?)`, serverID, revision, hash, agentID, serverID, revision, hash).Scan(&known); err != nil {
			return err
		}
		if !known {
			return errors.New("应用回执未匹配已发布配置")
		}
		var newestSequence, actualRevision int64
		var failed bool
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0),COALESCE(MAX(status='failed'),0) FROM node_applied_states WHERE server_id=? AND revision=? AND hash=?`, serverID, revision, hash).Scan(&newestSequence, &failed); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT applied_revision FROM agents WHERE id=? AND server_id=?`, agentID, serverID).Scan(&actualRevision); err != nil {
			return err
		}
		if actualRevision > revision || newestSequence > sequence || (status == "applied" && applyErr == "" && failed) {
			return ErrStaleApplication
		}
		var err error
		if status == "applied" && applyErr == "" {
			_, err = tx.ExecContext(ctx, `UPDATE agents SET applied_revision=?,applied_hash=?,apply_error='',updated_at=? WHERE id=? AND server_id=? AND applied_revision<=?`, revision, hash, fmtTime(s.Now()), agentID, serverID, revision)
		} else if status == "failed" || applyErr != "" {
			if applyErr == "" {
				applyErr = "配置应用失败"
			}
			_, err = tx.ExecContext(ctx, `UPDATE agents SET apply_error=?,updated_at=? WHERE id=? AND server_id=? AND applied_revision<=?`, applyErr, fmtTime(s.Now()), agentID, serverID, revision)
		}
		return err
	})
}

// AppliedSubscriptionNode returns a client projection only when the confirmed
// snapshot still matches current authority. Agent liveness is not an input.
func (s *Store) AppliedSubscriptionNode(ctx context.Context, n domain.Node) (domain.Node, bool, error) {
	return s.appliedSubscriptionNode(ctx, s.db, n)
}

func (s *Store) appliedSubscriptionNode(ctx context.Context, q querier, n domain.Node) (domain.Node, bool, error) {
	if !n.Enabled || n.Revoked {
		return n, false, nil
	}
	if n.ServerID == nil || n.Source != domain.NodeDeployed {
		return n, true, nil
	}
	sv, err := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, *n.ServerID))
	if err != nil {
		return n, false, err
	}
	if !sv.Enabled {
		return n, false, nil
	}
	var revision int64
	var payload, status string
	err = q.QueryRowContext(ctx, `SELECT revision,status,payload FROM node_applied_states WHERE server_id=? AND node_id=?`, sv.ID, n.ID).Scan(&revision, &status, &payload)
	if isNoRows(err) {
		return n, false, nil
	}
	if err != nil {
		return n, false, err
	}
	if status != "applied" {
		return n, false, nil
	}
	raw, err := s.decrypt(applicationLabel(sv.ID, n.ID, revision), payload)
	if err != nil {
		return n, false, err
	}
	var snapshot nodeApplicationSnapshot
	if err = json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return n, false, err
	}
	old := snapshot.Node
	if snapshot.Context != applicationContext(sv, snapshot.Spec) || old.Protocol != n.Protocol || old.Port != n.Port || old.ListenPort != n.ListenPort || old.Core != n.Core || old.NetworkRevision != n.NetworkRevision || !reflect.DeepEqual(old.Network, n.Network) || !reflect.DeepEqual(old.ShareID, n.ShareID) || !equalApplicationJSON(snapshot.ServerParams, n.ServerParams) || !equalApplicationJSON(old.Params, n.Params) {
		return n, false, nil
	}
	// A literal/explicit advertised endpoint is part of the client projection.
	// Inherited dynamic IP observation does not require a proxy restart.
	if (n.Network != nil && n.Network.AdvertiseMode != "inherit") && old.Server != n.Server {
		return n, false, nil
	}
	if n.Network != nil && n.Network.EgressProfileID != 0 {
		var enabled bool
		err = q.QueryRowContext(ctx, `SELECT enabled FROM egress_profiles WHERE id=? AND server_id=?`, n.Network.EgressProfileID, sv.ID).Scan(&enabled)
		if isNoRows(err) {
			return n, false, nil
		}
		if err != nil {
			return n, false, err
		}
		if !enabled {
			return n, false, nil
		}
	}
	// Return the stored credential projection with current display metadata.
	old.Server = n.Server
	old.Name = n.Name
	old.Tags = n.Tags
	old.ChainFrontNodeID = n.ChainFrontNodeID
	return old, true, nil
}

// Chain origins retain identity across host changes, rotations and deletion.
func (s *Store) ChainLanding(ctx context.Context, chainID int64) (domain.Node, bool, error) {
	var id sql.NullInt64
	var resolved bool
	err := s.db.QueryRowContext(ctx, `SELECT landing_id,resolved FROM node_chain_origins WHERE chain_id=?`, chainID).Scan(&id, &resolved)
	if isNoRows(err) {
		return domain.Node{}, false, nil
	}
	if err != nil {
		return domain.Node{}, false, err
	}
	if !resolved {
		err := s.Tx(ctx, func(tx *sql.Tx) error { return s.resolveChainOrigin(ctx, tx, chainID) })
		if err != nil {
			return domain.Node{}, false, err
		}
		return s.ChainLanding(ctx, chainID)
	}
	if !id.Valid {
		return domain.Node{}, false, nil
	}
	n, err := s.GetNode(ctx, id.Int64)
	return n, err == nil, err
}

func (s *Store) CreateChainNode(ctx context.Context, n *domain.Node, landingID int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.createNode(ctx, tx, n); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE node_chain_origins SET landing_id=?,resolved=1 WHERE chain_id=?`, landingID, n.ID)
		return err
	})
}

// A historical copied chain carries no original ID. Only one exact decrypted
// client projection may establish that identity; ambiguous/missing origins are
// permanently fenced, so deleting a landing cannot bind a later replacement.
func (s *Store) backfillChainOrigins(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT chain_id FROM node_chain_origins WHERE resolved=0`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.resolveChainOrigin(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) resolveChainOrigin(ctx context.Context, tx *sql.Tx, chainID int64) error {
	chain, err := s.scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=? AND source='chain'`, chainID))
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE source IN ('manual','deployed','imported') AND protocol=? AND server=? AND port=?`, chain.Protocol, chain.Server, chain.Port)
	if err != nil {
		return err
	}
	var matches []int64
	for rows.Next() {
		n, err := s.scanNode(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if equalApplicationJSON(n.Params, chain.Params) {
			matches = append(matches, n.ID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var landing any
	if len(matches) == 1 {
		landing = matches[0]
	}
	_, err = tx.ExecContext(ctx, `UPDATE node_chain_origins SET landing_id=?,resolved=1 WHERE chain_id=? AND resolved=0`, landing, chainID)
	return err
}
