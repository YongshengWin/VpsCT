package store

import (
	"context"
	"errors"

	"ctlvps/internal/domain"
)

var ErrNodePortRequest = errors.New("端口必须为 1–65535；mieru 端口须大于 1024，仅支持未撤销的受管部署节点")

// Port edits use the existing versioned network operation and publication
// outbox, so subscriptions remain fenced until the agent acknowledges them.
func (s *Store) validateNodePort(ctx context.Context, q querier, n domain.Node, port int) error {
	if port < 1 || port > 65535 || n.Source != domain.NodeDeployed || n.ServerID == nil || n.Revoked || (n.Protocol == "mieru" && port < 1025) {
		return ErrNodePortRequest
	}
	if port == n.ListenPort {
		return nil
	}
	if err := requireNoAccessRoutes(ctx, q, n.ID, false); err != nil {
		return err
	}
	var managed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_transits WHERE entry_node_id=? AND stage<>'retired')`, n.ID).Scan(&managed); err != nil {
		return err
	}
	if managed {
		return ErrManagedTransit
	}
	var reserved bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_listener_reservations WHERE server_id=? AND listen_port=? AND NOT(resource_kind='node' AND resource_id=?))`, *n.ServerID, port, n.ID).Scan(&reserved); err != nil {
		return err
	}
	if reserved {
		return ErrListenPortConflict
	}
	n.ListenPort, n.Port = port, port
	// Validate paused nodes too: enabling later must not expose an invalid pool.
	return s.validateNATNode(ctx, q, n)
}
