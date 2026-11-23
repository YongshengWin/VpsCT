package agentproto

import (
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"errors"
)

// NodeApplication contains no credentials. The controller resolves its client
// projection from the published revision rather than trusting agent parameters.
type NodeApplication struct {
	TLSTransportVersion int    `json:"tls_transport_version,omitempty"` // positive only after successful negotiated sing-box activation
	CoreVersion         string `json:"core_version,omitempty"`          // verified official executable, required for applied Xray
	NodeID              int64  `json:"node_id"`
	Core                string `json:"core"`
	ListenIPv6          string `json:"listen_ipv6,omitempty"`
	Status              string `json:"status"` // applied | blocked | failed
}

func ValidateNodeApplications(items []NodeApplication) error {
	if len(items) > 4096 {
		return errors.New("节点应用回执过多")
	}
	seen := map[int64]bool{}
	for _, item := range items {
		if item.NodeID <= 0 || item.NodeID > 1<<53 || seen[item.NodeID] || (item.Core != "singbox" && item.Core != "snell" && item.Core != "mieru" && item.Core != "xray") || (item.Status != "applied" && item.Status != "blocked" && item.Status != "failed") {
			return errors.New("节点应用回执无效")
		}
		if (item.Core == "xray" && item.Status == "applied" && item.CoreVersion != domain.DefaultXrayVersion) || ((item.Core != "xray" || item.Status != "applied") && item.CoreVersion != "") {
			return errors.New("节点应用回执内核版本无效")
		}
		if item.TLSTransportVersion != 0 && (item.Core != "singbox" || item.Status != "applied" || item.TLSTransportVersion != TLSTransportVersion) {
			return errors.New("TLS 传输回执无效")
		}
		if item.ListenIPv6 != "" && (item.Core != "singbox" || item.Status != "applied" || !networkconfig.IsPublicIPv6(item.ListenIPv6)) {
			return errors.New("仅 IPv6 监听回执无效")
		}
		seen[item.NodeID] = true
	}
	return nil
}
