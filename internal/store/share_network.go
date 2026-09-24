package store

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"database/sql"
	"errors"
)

// Snapshot share policies when allocating a new dedicated node. Existing
// nodes keep their own immutable revisions, including on revoke/reissue.
func (s *Store) CreateShareNode(ctx context.Context, n *domain.Node) error {
	if n.Network == nil {
		return s.CreateNode(ctx, n)
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if n.Network.AdvertiseMode != "inherit" {
			return errors.New("分享目标的访问地址须继承服务器")
		}
		if err := s.createNode(ctx, tx, n); err != nil {
			return err
		}
		view, err := s.previewNodeNetwork(ctx, tx, *n, n.Network)
		if err != nil {
			return err
		}
		if !view.Ready {
			return ErrNetworkNotReady
		}
		return nil
	})
}
func (s *Store) ValidateShareNetwork(ctx context.Context, targets []domain.ShareTarget) error {
	seen := map[int64]bool{}
	for _, t := range targets {
		if seen[t.ServerID] {
			return errors.New("分享目标服务器不能重复")
		}
		seen[t.ServerID] = true
		if t.Network == nil {
			continue
		}
		if err := t.Network.Validate(); err != nil {
			return err
		}
		if t.Network.AdvertiseMode != "inherit" {
			return errors.New("分享网络模板须继承服务器访问地址")
		}
		for _, p := range t.Protocols {
			core := domain.CoreFor(p, domain.CoreModeStable)
			_, version := bindingCoreSetting(core)
			spec := agentproto.NodeSpec{Protocol: p, Core: string(core)}
			if !agentproto.DirectBindingSupported(spec, version) && !(t.Network.EgressProfileID == 0 && agentproto.ListenBindingSupported(spec, version)) {
				return errors.New("分享网络继承的入口协议暂不支持绑定")
			}
		}
		if t.Network.EgressProfileID != 0 {
			var kind string
			if err := s.db.QueryRowContext(ctx, `SELECT kind FROM egress_profiles WHERE id=? AND server_id=?`, t.Network.EgressProfileID, t.ServerID).Scan(&kind); err != nil {
				return err
			}
			if kind == "wireguard" {
				return errors.New("WireGuard Peer 不可被多个分享复用，请在生成的节点上配置独立 Peer")
			}
			if kind != "direct" {
				for _, p := range t.Protocols {
					if p != domain.ProtocolShadowsocks {
						return errors.New("经中转出口的分享入口目前需要 SS-2022 AES-128")
					}
				}
			}
		}
	}
	return nil
}
