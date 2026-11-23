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

var ErrTLSTransportUnavailable = errors.New("普通 TLS 传输部署条件未满足")

// TLSTransportBlockReason is shared by the public server DTO and admission.
func TLSTransportBlockReason(_ domain.Server, a domain.Agent, now time.Time) string {
	if a.TokenHash == "" || a.LastSeenAt == nil || now.Sub(*a.LastSeenAt) > NetworkReadinessMaxAge || now.Sub(*a.LastSeenAt) < -time.Second {
		return "需要已注册的 agent 在最近两分钟内上报主机能力"
	}
	var d agentproto.Diagnostics
	var m agentproto.Metrics
	if json.Unmarshal(a.Diagnostics, &d) != nil || d.TLSTransportVersion != agentproto.TLSTransportVersion {
		return "请升级 agent 并等待其上报普通 TLS 传输能力"
	}
	if json.Unmarshal(a.Metrics, &m) != nil || m.Arch != "amd64" {
		return "普通 TLS 传输需要 Linux AMD64"
	}
	if d.SecurityPaused || !d.SecurityPolicy || !d.Nftables || d.MeteringError != "" {
		return "普通 TLS 传输需要可用的本机安全策略和 nftables 计量"
	}
	return ""
}

func tlsTransportParams(n domain.Node) (map[string]any, bool, error) {
	if n.Source != domain.NodeDeployed || (n.Protocol != domain.ProtocolVLESS && n.Protocol != domain.ProtocolTrojan) || len(n.ServerParams) == 0 {
		return nil, false, nil
	}
	var p map[string]any
	if err := json.Unmarshal(n.ServerParams, &p); err != nil {
		return nil, false, err
	}
	return p, agentproto.NeedsTLSTransport(n.Protocol, p), nil
}

// Called in the same write transaction as the node. The persistent version
// protects deletion/revocation as well as provision and credential rotation.
func (s *Store) admitTLSTransport(ctx context.Context, q querier, n domain.Node) error {
	p, needed, err := tlsTransportParams(n)
	if err != nil || !needed {
		return err
	}
	if n.ServerID == nil || n.Network != nil {
		return fmt.Errorf("%w: 需要普通受管接入，不能指定网络绑定或出口", ErrTLSTransportUnavailable)
	}
	if err := agentproto.ValidateTLSTransportParams(n.Protocol, string(n.Core), p); err != nil {
		return fmt.Errorf("%w: %w", ErrTLSTransportUnavailable, err)
	}
	sv, err := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id=?`, *n.ServerID))
	if err != nil {
		return err
	}
	a, err := scanAgent(q.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE server_id=?`, sv.ID))
	if err != nil {
		return err
	}
	if why := TLSTransportBlockReason(sv, a, s.Now()); why != "" {
		return fmt.Errorf("%w: %s", ErrTLSTransportUnavailable, why)
	}
	_, err = q.ExecContext(ctx, `INSERT INTO server_tls_transport_requirements(server_id,version) VALUES(?,?) ON CONFLICT DO NOTHING`, sv.ID, agentproto.TLSTransportVersion)
	return err
}

func (s *Store) TLSTransportVersion(ctx context.Context, serverID int64) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM server_tls_transport_requirements WHERE server_id=?`, serverID).Scan(&v)
	if isNoRows(err) {
		return 0, nil
	}
	return v, err
}
