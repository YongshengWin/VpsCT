package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

var ErrEgressCapacity = errors.New("该服务器的出口配置数量已达上限")
var ErrEgressRequest = errors.New("出口请求无效")

type EgressProfileRequest struct {
	ID               string                           `json:"operation_id"`
	Action           string                           `json:"action"`
	ServerID         int64                            `json:"server_id"`
	ProfileID        int64                            `json:"profile_id"`
	ExpectedRevision int64                            `json:"expected_revision"`
	Name             string                           `json:"name"`
	Kind             string                           `json:"kind"`
	Enabled          bool                             `json:"enabled"`
	Config           json.RawMessage                  `json:"config"`
	ExpectedImpact   string                           `json:"expected_impact,omitempty"`
	Credentials      *networkconfig.SOCKS5Credentials `json:"credentials,omitempty"`
}

func (in *EgressProfileRequest) normalize() (string, error) {
	if in.ExpectedImpact != "" && !validNetworkImpactToken(in.ExpectedImpact) {
		return "", ErrNetworkImpactRequired
	}
	if !networkconfig.ValidIdentity(in.ID) || in.ServerID < 0 || in.ServerID > 1<<53-1 || in.ProfileID < 0 || in.ProfileID > 1<<53-1 {
		return "", ErrEgressRequest
	}
	switch in.Action {
	case "create":
		if in.ServerID == 0 || in.ProfileID != 0 || in.ExpectedRevision != 0 {
			return "", ErrEgressRequest
		}
	case "update", "delete":
		if in.ServerID != 0 || in.ProfileID == 0 || in.ExpectedRevision < 1 || in.ExpectedRevision > MaxEgressRevisions {
			return "", ErrEgressRequest
		}
	default:
		return "", ErrEgressRequest
	}
	if in.Action == "delete" {
		if in.Name != "" || in.Kind != "" || in.Enabled || len(in.Config) != 0 || in.Credentials != nil {
			return "", ErrEgressRequest
		}
	} else {
		var err error
		in.Name, in.Config, err = validateEgress(in.Name, in.Kind, in.Config)
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrEgressRequest, err)
		}
		if in.Kind == "socks5" {
			cfg, _ := networkconfig.DecodeSOCKS5(in.Config)
			if in.Credentials != nil {
				if err := in.Credentials.Validate(cfg.Authentication); err != nil {
					return "", fmt.Errorf("%w: %s", ErrEgressRequest, err)
				}
			} else if in.Action == "create" && cfg.Authentication == "password" {
				return "", fmt.Errorf("%w: 请提供 SOCKS5 上游用户名和密码", ErrEgressRequest)
			}
		} else if in.Kind == "ss2022" {
			cfg, _ := networkconfig.DecodeSS2022(in.Config)
			if in.Credentials != nil {
				if err := in.Credentials.ValidateSS2022(cfg.Method); err != nil {
					return "", fmt.Errorf("%w: %s", ErrEgressRequest, err)
				}
			} else if in.Action == "create" {
				return "", fmt.Errorf("%w: 请提供 SS-2022 密钥", ErrEgressRequest)
			}
		} else if in.Kind == "wireguard" {
			cfg, _ := networkconfig.DecodeWireGuard(in.Config)
			if in.Credentials != nil {
				if err := in.Credentials.ValidateWireGuard(cfg); err != nil {
					return "", fmt.Errorf("%w: %s", ErrEgressRequest, err)
				}
			} else if in.Action == "create" {
				return "", fmt.Errorf("%w: 请提供 WireGuard 凭据", ErrEgressRequest)
			}
		} else if in.Kind == "ssh" {
			cfg, _ := networkconfig.DecodeSSH(in.Config)
			if in.Credentials != nil {
				if err := in.Credentials.ValidateSSH(cfg); err != nil {
					return "", fmt.Errorf("%w: %s", ErrEgressRequest, err)
				}
			} else if in.Action == "create" {
				return "", fmt.Errorf("%w: 请提供 SSH 转发凭据", ErrEgressRequest)
			}
		} else if in.Credentials != nil {
			return "", ErrEgressRequest
		}
		if in.Action == "update" && in.ExpectedRevision == MaxEgressRevisions {
			return "", fmt.Errorf("%w: 出口版本已达上限", ErrEgressRequest)
		}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:]), nil
}

// RequestEgressProfile keeps the mutation, idempotency receipt, publication
// intent and audit in one transaction. A saved template is not a live binding.
func (s *Store) RequestEgressProfile(ctx context.Context, in EgressProfileRequest, audit domain.AuditEvent) (NetworkOperation, error) {
	return s.requestEgressProfile(ctx, in, audit, false)
}

func (s *Store) RequestReviewedEgressProfile(ctx context.Context, in EgressProfileRequest, audit domain.AuditEvent) (NetworkOperation, error) {
	return s.requestEgressProfile(ctx, in, audit, true)
}

func (s *Store) requestEgressProfile(ctx context.Context, in EgressProfileRequest, audit domain.AuditEvent, requireReview bool) (NetworkOperation, error) {
	hash, err := in.normalize()
	if err != nil {
		return NetworkOperation{}, err
	}
	kind := "egress_" + in.Action
	var op NetworkOperation
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		previous, err := scanNetworkOperation(tx.QueryRowContext(ctx, `SELECT `+networkOperationCols+` FROM network_operations WHERE id=?`, in.ID))
		if err == nil {
			if previous.Kind != kind || previous.RequestHash != hash {
				return ErrNetworkOperationConflict
			}
			op = previous
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		p := domain.EgressProfile{ServerID: in.ServerID, Name: in.Name, Kind: in.Kind, Enabled: in.Enabled}
		if in.Action != "create" {
			p, err = scanEgress(tx.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=?`, in.ProfileID))
			if err != nil {
				return err
			}
			if p.CurrentRevision != in.ExpectedRevision {
				return ErrNetworkConflict
			}
			if in.Action == "update" && p.Kind != in.Kind {
				return fmt.Errorf("%w: 出口类型不能在原配置上更换", ErrEgressRequest)
			}
		} else {
			var found int
			if err := tx.QueryRowContext(ctx, `SELECT id FROM servers WHERE id=?`, p.ServerID).Scan(&found); isNoRows(err) {
				return ErrNotFound
			} else if err != nil {
				return err
			}
		}
		if err := networkMaintenance(ctx, tx, p.ServerID); err != nil {
			return err
		}
		if requireReview && in.ExpectedImpact == "" {
			return ErrNetworkImpactRequired
		}
		if in.ExpectedImpact != "" {
			impact, err := s.egressNetworkImpact(ctx, tx, p, in)
			if err != nil {
				return err
			}
			if impact.Token != in.ExpectedImpact {
				return ErrNetworkImpactChanged
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM network_operations WHERE server_id=?`, p.ServerID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxNetworkOperations {
			return ErrNetworkOperationCapacity
		}
		var refs int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM node_networks WHERE egress_profile_id=?`, p.ID).Scan(&refs); err != nil {
			return err
		}
		var forwardRefs int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM port_forwards WHERE egress_profile_id=?`, p.ID).Scan(&forwardRefs); err != nil {
			return err
		}
		changesLiveState := in.Action == "update" && p.Enabled != in.Enabled && refs+forwardRefs > 0
		switch in.Action {
		case "create":
			p, err = s.createEgressProfileWithCredentials(ctx, tx, p, in.Config, in.Credentials)
		case "update":
			p, err = s.appendEgressRevisionWithCredentials(ctx, tx, p.ID, in.ExpectedRevision, in.Name, in.Enabled, in.Config, in.Credentials)
		case "delete":
			err = s.deleteEgressProfile(ctx, tx, p.ID, in.ExpectedRevision)
		}
		if err != nil {
			return err
		}
		// Disabling can be queued while offline. Enabling resumes every pinned
		// version, including paused nodes, and must validate those old versions.
		if changesLiveState && in.Enabled {
			if err := s.checkEgressConsumers(ctx, tx, p.ID); err != nil {
				return err
			}
		}
		now := s.Now()
		op = NetworkOperation{ID: in.ID, ServerID: p.ServerID, Kind: kind, ResourceID: p.ID, ResourceRevision: p.CurrentRevision, RequestHash: hash,
			Status: "saved", Message: "出口配置已保存，现有节点绑定保持原版本", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		if in.Action == "delete" {
			op.Message = "未被引用的出口配置已删除"
		}
		if changesLiveState {
			op.Generation, err = networkGeneration(ctx, tx, p.ServerID)
			if err != nil {
				return err
			}
			if op.Generation < 1 {
				return errors.New("missing network publication generation")
			}
			op.Status, op.Message = "queued", "出口启停已保存，等待 agent 应用；当前业务状态以回执为准"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO network_operations(id,server_id,kind,resource_id,resource_revision,generation,request_hash,status,next_attempt_at,message,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, op.ServerID, op.Kind, op.ResourceID, op.ResourceRevision, op.Generation, hash, op.Status, networkOperationTime(now), op.Message, networkOperationTime(now), networkOperationTime(now))
		if err != nil {
			return err
		}
		audit.Action, audit.Target = "egress."+in.Action, fmt.Sprint(p.ID)
		audit.Detail, _ = json.Marshal(map[string]any{"operation_id": op.ID, "profile_id": p.ID, "revision": p.CurrentRevision, "generation": op.Generation, "affected_nodes": refs, "affected_forwards": forwardRefs, "enabled": p.Enabled})
		return s.addAudit(ctx, tx, audit)
	})
	if err != nil {
		if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
			err = ErrNetworkConflict
		}
		return NetworkOperation{}, err
	}
	return op, nil
}

func (s *Store) checkEgressConsumers(ctx context.Context, tx *sql.Tx, id int64) error {
	// Page the IDs to bound working memory without skipping disabled consumers.
	var after int64
	for {
		rows, err := tx.QueryContext(ctx, `SELECT node_id FROM node_networks WHERE egress_profile_id=? AND node_id>? ORDER BY node_id LIMIT 100`, id, after)
		if err != nil {
			return err
		}
		ids := []int64{}
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
		if len(ids) == 0 {
			var serverID int64
			err := tx.QueryRowContext(ctx, `SELECT server_id FROM port_forwards WHERE egress_profile_id=? LIMIT 1`, id).Scan(&serverID)
			if isNoRows(err) {
				return nil
			}
			if err != nil {
				return err
			}
			state, err := s.networkReadiness(ctx, tx, serverID)
			if err != nil {
				return err
			}
			if !state.view.Ready || state.view.ForwardVersion != agentproto.NetworkForwardVersion {
				return ErrNetworkNotReady
			}
			return nil
		}
		for _, nodeID := range ids {
			n, err := s.scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, nodeID))
			if err != nil {
				return err
			}
			view, err := s.previewNodeNetwork(ctx, tx, n, n.Network)
			if err != nil {
				return err
			}
			if !view.Ready {
				return ErrNetworkNotReady
			}
		}
		after = ids[len(ids)-1]
	}
}

type EgressReference struct {
	NodeID   int64  `json:"node_id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
}
type EgressProfileView struct {
	Profile               domain.EgressProfile   `json:"profile"`
	Revision              domain.EgressRevision  `json:"revision"`
	References            []EgressReference      `json:"references"`
	ReferenceCount        int                    `json:"reference_count"`
	Offset                int                    `json:"offset"`
	Limit                 int                    `json:"limit"`
	ForwardReferences     []NetworkImpactForward `json:"forward_references,omitempty"`
	ForwardReferenceCount int                    `json:"forward_reference_count,omitempty"`
}

func (s *Store) EgressProfileView(ctx context.Context, id int64, limit, offset int) (EgressProfileView, error) {
	v := EgressProfileView{References: []EgressReference{}, Limit: max(1, min(limit, 100)), Offset: max(0, min(offset, 10000))}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		v.Profile, err = scanEgress(tx.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=?`, id))
		if err != nil {
			return err
		}
		var raw, created string
		v.Revision.ProfileID, v.Revision.ServerID, v.Revision.Revision = id, v.Profile.ServerID, v.Profile.CurrentRevision
		if err := tx.QueryRowContext(ctx, `SELECT r.config,r.created_at,EXISTS(SELECT 1 FROM egress_profile_credentials c WHERE c.server_id=r.server_id AND c.profile_id=r.profile_id AND c.revision=r.revision) FROM egress_profile_revisions r WHERE r.profile_id=? AND r.revision=?`, id, v.Profile.CurrentRevision).Scan(&raw, &created, &v.Revision.HasCredentials); err != nil {
			return err
		}
		v.Revision.Config, v.Revision.CreatedAt = json.RawMessage(raw), parseTime(created)
		forwards, err := s.impactForwards(ctx, tx, v.Profile.ServerID)
		if err != nil {
			return err
		}
		for _, f := range forwards {
			if f.EgressProfileID == id {
				v.ForwardReferences = append(v.ForwardReferences, f)
				v.ForwardReferenceCount++
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM node_networks WHERE egress_profile_id=?`, id).Scan(&v.ReferenceCount); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT n.id,n.name,n.enabled,b.egress_revision FROM node_networks b JOIN nodes n ON n.id=b.node_id WHERE b.egress_profile_id=? ORDER BY n.id LIMIT ? OFFSET ?`, id, v.Limit, v.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r EgressReference
			if err := rows.Scan(&r.NodeID, &r.Name, &r.Enabled, &r.Revision); err != nil {
				return err
			}
			v.References = append(v.References, r)
		}
		return rows.Err()
	})
	return v, err
}
