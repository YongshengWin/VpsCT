package netinventory

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

// ResolveSOCKS5WithDNS queries only the configured bootstrap resolver, then
// pins one validated address for this application. Core DNS cannot replace it.
func ResolveSOCKS5WithDNS(ctx context.Context, nodeID int64, n networkconfig.Node, cfg networkconfig.SOCKS5, snapshot *agentproto.NetworkSnapshot, ipv4Only bool, now time.Time) (networkconfig.Resolved, error) {
	return ResolveSOCKS5WithGrants(ctx, nodeID, n, cfg, snapshot, ipv4Only, now, nil)
}

func ResolveSOCKS5WithGrants(ctx context.Context, nodeID int64, n networkconfig.Node, cfg networkconfig.SOCKS5, snapshot *agentproto.NetworkSnapshot, ipv4Only bool, now time.Time, grants []networkconfig.TransportGrant) (networkconfig.Resolved, error) {
	if _, err := netip.ParseAddr(cfg.Server); err == nil {
		return resolveLiteralSOCKS5(nodeID, n, cfg, snapshot, ipv4Only, now, grants)
	}
	var empty networkconfig.Resolved
	cfg, err := networkconfig.EffectiveSOCKS5(cfg, ipv4Only)
	if err != nil {
		return empty, err
	}
	r, err := ResolveBinding(n, &cfg.Outer, snapshot, ipv4Only, now)
	if err != nil {
		return empty, err
	}
	if err := ValidateBootstrapEndpoint(nodeID, n.EgressProfileID, cfg.Outer.DNS, snapshot, grants); err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	families := []string{cfg.Outer.Family}
	if cfg.Outer.Family == "dual" {
		families = []string{"ipv4", "ipv6"}
	}
	host := strings.ToLower(strings.TrimSuffix(cfg.Server, "."))
	for _, family := range families {
		started := time.Now()
		result, err := LookupBootstrapWithGrants(ctx, nodeID, n.EgressProfileID, host, family, *r.Direct, grants)
		if err != nil {
			return empty, err
		}
		sort.Strings(result.Addresses)
		for _, address := range result.Addresses {
			candidate := networkconfig.ResolvedSOCKS5{Purpose: cfg.Purpose, Host: host, Address: address, Port: cfg.ServerPort, UDP: cfg.UDP}
			candidate.SetDNSLifetime(started, result.TTLSeconds)
			candidate, err = candidate.WithTransportGrants(nodeID, n.EgressProfileID, grants)
			if err != nil {
				return empty, err
			}
			if err := ValidateSOCKS5Endpoint(candidate, snapshot); err != nil {
				return empty, err // mixed public/private answers do not gain a bypass
			}
		}
		if len(result.Addresses) > 0 {
			r.SOCKS5 = &networkconfig.ResolvedSOCKS5{Purpose: cfg.Purpose, Host: host, Address: result.Addresses[0], Port: cfg.ServerPort, UDP: cfg.UDP}
			r.SOCKS5.SetDNSLifetime(started, result.TTLSeconds)
			approved, err := r.SOCKS5.WithTransportGrants(nodeID, n.EgressProfileID, grants)
			if err != nil {
				return empty, err
			}
			r.SOCKS5 = &approved
			return r, nil
		}
	}
	return empty, errors.New("启动 DNS 没有返回所选外层地址族的可用地址")
}

// ResolveSOCKS5 pins the upstream before core application. Literal endpoints
// are supported now; hostname resolution must produce the same guarded pin
// before domain profiles are admitted, never resolve freely inside the core.
func ResolveSOCKS5(n networkconfig.Node, cfg networkconfig.SOCKS5, snapshot *agentproto.NetworkSnapshot, ipv4Only bool, now time.Time) (networkconfig.Resolved, error) {
	return resolveLiteralSOCKS5(0, n, cfg, snapshot, ipv4Only, now, nil)
}

func resolveLiteralSOCKS5(nodeID int64, n networkconfig.Node, cfg networkconfig.SOCKS5, snapshot *agentproto.NetworkSnapshot, ipv4Only bool, now time.Time, grants []networkconfig.TransportGrant) (networkconfig.Resolved, error) {
	var empty networkconfig.Resolved
	cfg, err := networkconfig.EffectiveSOCKS5(cfg, ipv4Only)
	if err != nil {
		return empty, err
	}
	ip, err := networkconfig.HostAddress(cfg.Server)
	if err != nil {
		return empty, errors.New("SOCKS5 域名上游的受限启动解析尚未就绪")
	}
	endpoint := networkconfig.ResolvedSOCKS5{Purpose: cfg.Purpose, Address: ip.String(), Port: cfg.ServerPort, UDP: cfg.UDP}
	endpoint, err = endpoint.WithTransportGrants(nodeID, n.EgressProfileID, grants)
	if err != nil {
		return empty, err
	}
	if err = ValidateSOCKS5Endpoint(endpoint, snapshot); err != nil {
		return empty, err
	}
	r, err := ResolveBinding(n, &cfg.Outer, snapshot, ipv4Only, now)
	if err != nil {
		return empty, err
	}
	r.SOCKS5 = &endpoint
	return r, nil
}

// A formerly remote address becoming local invalidates the transport rather
// than turning an upstream grant into access to the host's own services.
func ValidateSOCKS5Endpoint(endpoint networkconfig.ResolvedSOCKS5, snapshot *agentproto.NetworkSnapshot) error {
	if err := endpoint.Validate(); err != nil {
		return err
	}
	return validateRemoteTransportAddress(endpoint.Address, snapshot)
}

func validateRemoteTransportAddress(address string, snapshot *agentproto.NetworkSnapshot) error {
	if snapshot == nil {
		return errors.New("上游端点校验缺少本机接口清单")
	}
	ip, _ := netip.ParseAddr(address)
	for _, iface := range snapshot.Interfaces {
		for _, raw := range iface.Addresses {
			prefix, err := netip.ParsePrefix(raw)
			if err == nil && prefix.Addr().Unmap() == ip {
				return errors.New("上游不能指向本机服务")
			}
		}
	}
	return nil
}

// UDP bootstrap DNS can retry TCP on truncation. Both transports must be
// independently authorized at exactly this resolver port before opening it.
func ValidateBootstrapEndpoint(nodeID, profileID int64, resolver networkconfig.Resolver, snapshot *agentproto.NetworkSnapshot, grants []networkconfig.TransportGrant) error {
	if resolver.Transport != "tcp" && resolver.Transport != "udp" {
		return errors.New("启动 DNS 传输协议无效")
	}
	ip, err := networkconfig.HostAddress(resolver.Address)
	if err != nil {
		return err
	}
	if networkconfig.NeedsTransportGrant(ip) {
		for _, network := range []string{"tcp", resolver.Transport} {
			if !networkconfig.AuthorizeTransport(grants, nodeID, profileID, "bootstrap_dns", network, ip, resolver.Port) {
				return errors.New("私网启动 DNS 缺少对应节点、出口及 TCP/UDP 端口的本机授权")
			}
		}
	} else if err := (networkconfig.ResolvedSOCKS5{Address: ip.String(), Port: resolver.Port}).Validate(); err != nil {
		return err
	}
	return validateRemoteTransportAddress(ip.String(), snapshot)
}
