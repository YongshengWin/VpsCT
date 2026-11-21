package agentproto

import (
	"ctlvps/internal/networkconfig"
	"errors"
)

// NodeApplication contains no credentials. The controller resolves its client
// projection from the published revision rather than trusting agent parameters.
type NodeApplication struct {
	NodeID     int64  `json:"node_id"`
	Core       string `json:"core"`
	ListenIPv6 string `json:"listen_ipv6,omitempty"`
	Status     string `json:"status"` // applied | blocked | failed
}

func ValidateNodeApplications(items []NodeApplication) error {
	if len(items) > 4096 {
		return errors.New("节点应用回执过多")
	}
	seen := map[int64]bool{}
	for _, item := range items {
		if item.NodeID <= 0 || item.NodeID > 1<<53 || seen[item.NodeID] || (item.Core != "singbox" && item.Core != "snell" && item.Core != "mieru") || (item.Status != "applied" && item.Status != "blocked" && item.Status != "failed") {
			return errors.New("节点应用回执无效")
		}
		if item.ListenIPv6 != "" && (item.Core != "singbox" || item.Status != "applied" || !networkconfig.IsPublicIPv6(item.ListenIPv6)) {
			return errors.New("仅 IPv6 监听回执无效")
		}
		seen[item.NodeID] = true
	}
	return nil
}
