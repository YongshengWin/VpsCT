package core

import (
	"net/netip"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

func CompileWireGuard(nodeID int64, cfg networkconfig.WireGuard, secret networkconfig.SOCKS5Credentials, transport networkconfig.SOCKS5, outer networkconfig.ResolvedDirect) (SOCKS5Compilation, error) {
	return CompileResourceWireGuard(agentproto.ResourceIdentity{Kind: "node", ID: nodeID}, cfg, secret, transport, outer)
}

func CompileResourceWireGuard(resource agentproto.ResourceIdentity, cfg networkconfig.WireGuard, secret networkconfig.SOCKS5Credentials, transport networkconfig.SOCKS5, outer networkconfig.ResolvedDirect) (SOCKS5Compilation, error) {
	var out SOCKS5Compilation
	if err := secret.ValidateWireGuard(cfg); err != nil {
		return out, err
	}
	out, err := CompileResourceSOCKS5(resource, transport, networkconfig.SOCKS5Credentials{}, outer)
	if err != nil {
		return out, err
	}
	// The native WireGuard listener owns its SO_MARK and wildcard bind address.
	// Use its client bind through a node-owned direct detour so the regular
	// dialer preserves the exact source/interface/mark enforced by our guard.
	direct, err := CompileResourceDirect(resource, outer)
	if err != nil {
		return out, err
	}
	out.TransportOutbound = direct.Outbound
	base, _ := resource.Tag()
	out.TransportOutbound["tag"] = base + "-wireguard-transport"
	out.Outbound = map[string]any{"detour": out.TransportOutbound["tag"]}
	var addresses, allowed []string
	include := func(raw string) bool {
		p, _ := netip.ParsePrefix(raw)
		return transport.Family == "dual" || (transport.Family == "ipv4") == p.Addr().Is4()
	}
	for _, raw := range cfg.Addresses {
		if include(raw) {
			addresses = append(addresses, raw)
		}
	}
	for _, raw := range cfg.AllowedIPs {
		if include(raw) {
			allowed = append(allowed, raw)
		}
	}
	peer := map[string]any{"address": transport.Server, "port": transport.ServerPort, "public_key": cfg.PublicKey, "allowed_ips": allowed, "persistent_keepalive_interval": cfg.PersistentKeepalive}
	if secret.WireGuardPresharedKey != "" {
		peer["pre_shared_key"] = secret.WireGuardPresharedKey
	}
	if len(cfg.Reserved) > 0 {
		peer["reserved"] = cfg.Reserved
	}
	tag := base + "-wireguard"
	out.Outbound["type"], out.Outbound["tag"], out.Outbound["system"] = "wireguard", tag, false
	out.Outbound["address"], out.Outbound["private_key"], out.Outbound["peers"] = addresses, secret.WireGuardPrivateKey, []any{peer}
	out.Outbound["mtu"], out.Outbound["workers"] = cfg.MTU, 1
	out.DNS["detour"] = tag
	return out, nil
}
