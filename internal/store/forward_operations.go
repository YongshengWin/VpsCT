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

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

type PortForwardRequest struct {
	ID               string                 `json:"operation_id"`
	Action           string                 `json:"action"`
	ServerID         int64                  `json:"server_id"`
	ForwardID        int64                  `json:"forward_id"`
	ExpectedRevision int64                  `json:"expected_revision"`
	ExpectedImpact   string                 `json:"expected_impact,omitempty"`
	Name             string                 `json:"name"`
	Enabled          bool                   `json:"enabled"`
	Config           *networkconfig.Forward `json:"config,omitempty"`
}

func (in *PortForwardRequest) normalize() (string, error) {
	if in.ExpectedImpact != "" && !validNetworkImpactToken(in.ExpectedImpact) {
		return "", ErrNetworkImpactRequired
	}
	if !networkconfig.ValidIdentity(in.ID) || in.ServerID < 0 || in.ServerID > 1<<53-1 || in.ForwardID < 0 || in.ForwardID > 0xffffff {
		return "", errors.New("转发操作编号或服务器无效")
	}
	switch in.Action {
	case "create":
		if in.ServerID == 0 || in.ForwardID != 0 || in.ExpectedRevision != 0 {
			return "", errors.New("转发创建请求无效")
		}
	case "update", "delete":
		if in.ServerID != 0 || in.ForwardID == 0 || in.ExpectedRevision < 1 || in.ExpectedRevision >= MaxForwardRevisions {
			return "", errors.New("转发修改请求或版本无效")
		}
	default:
		return "", errors.New("转发操作类型无效")
	}
	if in.Action == "delete" {
		if in.Config != nil || in.Name != "" || in.Enabled {
			return "", errors.New("删除请求不能携带新的转发配置")
		}
	} else {
		if in.Config == nil {
			return "", errors.New("缺少转发配置")
		}
		f, _, err := validateForward(domain.PortForward{ServerID: max(1, in.ServerID), Name: in.Name, Config: *in.Config})
		if err != nil {
			return "", err
		}
		in.Name, in.Config = f.Name, &f.Config
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// RequestPortForward is the persistence primitive; HTTP uses the reviewed path.
func (s *Store) RequestPortForward(ctx context.Context, in PortForwardRequest, audit domain.AuditEvent) (NetworkOperation, error) {
	return s.requestPortForward(ctx, in, audit, false)
}

func (s *Store) RequestReviewedPortForward(ctx context.Context, in PortForwardRequest, audit domain.AuditEvent) (NetworkOperation, error) {
	return s.requestPortForward(ctx, in, audit, true)
}

func (s *Store) requestPortForward(ctx context.Context, in PortForwardRequest, audit domain.AuditEvent, reviewed bool) (NetworkOperation, error) {
	hash, err := in.normalize()
	if err != nil {
		return NetworkOperation{}, fmt.Errorf("%w: %w", ErrForwardRequest, err)
	}
	kind := "forward_" + in.Action
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
		f := domain.PortForward{ServerID: in.ServerID, Name: in.Name, Enabled: in.Enabled}
		if in.Action != "create" {
			f, err = scanForward(tx.QueryRowContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE id=?`, in.ForwardID))
			if err != nil {
				return err
			}
			if f.Revision != in.ExpectedRevision {
				return ErrNetworkConflict
			}
		}
		if err := networkMaintenance(ctx, tx, f.ServerID); err != nil {
			return err
		}
		if reviewed {
			if !validNetworkImpactToken(in.ExpectedImpact) {
				return ErrNetworkImpactRequired
			}
			view, err := s.previewPortForward(ctx, tx, f, in)
			if err != nil {
				return err
			}
			if view.Impact.Token != in.ExpectedImpact {
				return ErrNetworkImpactChanged
			}
			if !view.Ready {
				return ErrNetworkNotReady
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM network_operations WHERE server_id=?`, f.ServerID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxNetworkOperations {
			return ErrNetworkOperationCapacity
		}
		switch in.Action {
		case "create":
			f.Config = *in.Config
			f, err = s.createPortForward(ctx, tx, f)
		case "update":
			f, err = s.updatePortForward(ctx, tx, f.ID, in.ExpectedRevision, in.Name, in.Enabled, *in.Config)
		case "delete":
			f, err = s.retirePortForward(ctx, tx, f.ID, in.ExpectedRevision)
		}
		if err != nil {
			return err
		}
		generation, err := networkGeneration(ctx, tx, f.ServerID)
		if err != nil {
			return err
		}
		if generation < 1 {
			return errors.New("转发配置缺少发布代次")
		}
		now := s.Now()
		op = NetworkOperation{ID: in.ID, ServerID: f.ServerID, Kind: kind, ResourceID: f.ID, ResourceRevision: f.Revision, Generation: generation, RequestHash: hash,
			Status: "queued", Message: "转发配置已保存，等待兼容 agent 应用；端口仍保留至清理确认", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		_, err = tx.ExecContext(ctx, `INSERT INTO network_operations(id,server_id,kind,resource_id,resource_revision,generation,request_hash,status,next_attempt_at,message,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			op.ID, op.ServerID, op.Kind, op.ResourceID, op.ResourceRevision, generation, hash, op.Status, networkOperationTime(now), op.Message, networkOperationTime(now), networkOperationTime(now))
		if err != nil {
			return err
		}
		audit.Action, audit.Target = "forward."+in.Action, fmt.Sprint(f.ID)
		audit.Detail, _ = json.Marshal(map[string]any{"operation_id": op.ID, "forward_id": f.ID, "revision": f.Revision, "generation": generation})
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
