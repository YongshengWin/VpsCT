package agent

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Resolve only the camouflage handshake using the explicitly selected outer
// resolver. Pin the public result; business DNS and traffic stay in the tunnel.
func (r *bindingRuntime) resolveRealityHandshake(ctx context.Context, n agentproto.NodeSpec, snapshot *agentproto.NetworkSnapshot, ipv4 bool) (*networkconfig.ResolvedHandshake, error) {
	cfg, _ := n.Network.TransportConfig()
	host, _ := n.Params["handshake_server"].(string)
	if host == "" {
		return nil, errors.New("Reality 握手域名为空")
	}
	port := 443
	if raw, ok := n.Params["handshake_port"]; ok {
		switch value := raw.(type) {
		case float64:
			if value != float64(int(value)) {
				return nil, errors.New("Reality 握手端口须为整数")
			}
			port = int(value)
		case int:
			port = value
		default:
			return nil, errors.New("Reality 握手端口无效")
		}
	}
	cfg.Purpose = ""
	cfg.Server = host
	cfg.ServerPort = port
	cfg.UDP = false
	cfg.Authentication = "none"
	resolve := r.resolveSOCKS
	if resolve == nil {
		resolve = netinventory.ResolveSOCKS5WithGrants
	}
	pin, err := resolve(ctx, n.NodeID, n.Network.Policy, cfg, snapshot, ipv4, time.Now(), n.TransportGrants)
	if err != nil {
		return nil, fmt.Errorf("Reality 握手解析失败: %w", err)
	}
	if pin.SOCKS5 == nil {
		return nil, errors.New("Reality 握手没有解析结果")
	}
	result := &networkconfig.ResolvedHandshake{Host: strings.ToLower(strings.TrimSuffix(host, ".")), Address: pin.SOCKS5.Address, Port: port}
	if _, err := networkconfig.HostAddress(host); err != nil {
		result.RefreshAt = pin.SOCKS5.RefreshAt
		if result.RefreshAt.IsZero() {
			result.RefreshAt = time.Now().Add(30 * time.Second)
		}
	}
	// Clean validation deliberately cannot inherit private endpoint grants.
	if err = result.Validate(); err != nil {
		return nil, err
	}
	if err = netinventory.ValidateSOCKS5Endpoint(networkconfig.ResolvedSOCKS5{Address: result.Address, Port: port}, r.collect()); err != nil {
		return nil, err
	}
	return result, nil
}
