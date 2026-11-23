package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

var ErrXrayUnavailable = errors.New("XHTTP 部署条件未满足")
var ErrCoreModeInUse = errors.New("服务器设置与现有节点组合冲突")

// XrayBlockReason powers both the deployment dialog and transactional admission.
// The capability is explicitly advertised by a new agent; never infer it from
// a binary version string or from systemd-compatible OpenRC service status.
func XrayBlockReason(s domain.Server, a domain.Agent, now time.Time) string {
	if s.CoreMode != domain.CoreModeStable {
		return "XHTTP 需要标准模式，精简模式只运行 sing-box"
	}
	if s.IsNAT {
		return "XHTTP 只支持独立公网的普通接入"
	}
	if a.TokenHash == "" || a.LastSeenAt == nil || now.Sub(*a.LastSeenAt) > NetworkReadinessMaxAge || now.Sub(*a.LastSeenAt) < -time.Second {
		return "需要已注册的 agent 在最近两分钟内上报主机能力"
	}
	var d agentproto.Diagnostics
	var m agentproto.Metrics
	if json.Unmarshal(a.Diagnostics, &d) != nil || d.XrayVersion != agentproto.XrayVersion {
		return "请升级 agent 并等待其上报 XHTTP 部署能力"
	}
	if json.Unmarshal(a.Metrics, &m) != nil || m.Arch != "amd64" || !d.Systemd {
		return "XHTTP 目前需要 Linux AMD64 和 systemd"
	}
	if d.SecurityPaused || !d.SecurityPolicy || !d.Nftables || d.MeteringError != "" {
		return "XHTTP 需要可用的本机安全策略和 nftables 计量"
	}
	if d.NAT != nil && (d.NAT.Configured || d.NAT.Status == "error" || d.NAT.Status == "pending") {
		return "请先清除主机 NAT 配置并等待生效"
	}
	return ""
}

func (s *Store) validateDedicatedCore(ctx context.Context, q querier, n domain.Node) error {
	if n.Source != domain.NodeDeployed || n.ServerID == nil || (n.Core != domain.CoreXray && n.Core != domain.CoreSnell && n.Core != domain.CoreMita) {
		return nil
	}
	if n.Core != domain.CoreXray {
		var mode domain.CoreMode
		if err := q.QueryRowContext(ctx, `SELECT core_mode FROM servers WHERE id=?`, *n.ServerID).Scan(&mode); err != nil {
			return err
		}
		if mode == domain.CoreModeLean {
			return fmt.Errorf("%w: 精简模式不支持 %s", ErrCoreModeInUse, n.Core)
		}
		return nil
	}
	server, err := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, *n.ServerID))
	if err != nil {
		return err
	}
	if n.Network != nil || n.Protocol != domain.ProtocolVLESS {
		return fmt.Errorf("%w: 仅支持无自定义网络策略的 VLESS", ErrXrayUnavailable)
	}
	agent, err := scanAgent(q.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE server_id=?`, server.ID))
	if err != nil {
		return err
	}
	if why := XrayBlockReason(server, agent, s.Now()); why != "" {
		return fmt.Errorf("%w: %s", ErrXrayUnavailable, why)
	}
	return nil
}

// A downgraded agent cannot clean up the last Xray process, even when the
// newest desired no longer contains a node. Keep the floor across revocation.
func (s *Store) XrayVersion(ctx context.Context, serverID int64) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM server_xray_requirements WHERE server_id=?`, serverID).Scan(&v)
	if isNoRows(err) {
		return 0, nil
	}
	return v, err
}

func rejectIncompatibleCores(ctx context.Context, q querier, id int64, lean, nat bool) error {
	var ids string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(group_concat(id, ', '),'') FROM (SELECT id FROM nodes WHERE server_id=? AND source='deployed' AND revoked=0 AND ((? AND core IN ('xray','snell','mieru')) OR (? AND core='xray')) ORDER BY id)`, id, lean, nat).Scan(&ids)
	if err != nil {
		return err
	}
	if ids != "" {
		return fmt.Errorf("%w: 节点 ID %s；请先迁移或移除这些节点", ErrCoreModeInUse, ids)
	}
	return nil
}
