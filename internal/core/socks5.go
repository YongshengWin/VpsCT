package core

import (
	"errors"
	"fmt"
	"reflect"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

// SOCKS5Compilation is one consumer's independent upstream transport, bootstrap
// resolver and business resolver. It is not enabled by merely saving a profile.
type SOCKS5Compilation struct {
	Outbound          map[string]any
	TransportOutbound map[string]any // private dial path, never a business fallback
	BootstrapDNS      map[string]any
	DNS               map[string]any
	ResolveRule       map[string]any
	FamilyReject      map[string]any
	UDPReject         map[string]any
}

func CompileSOCKS5(nodeID int64, cfg networkconfig.SOCKS5, credentials networkconfig.SOCKS5Credentials, outer networkconfig.ResolvedDirect) (SOCKS5Compilation, error) {
	return CompileResourceSOCKS5(agentproto.ResourceIdentity{Kind: "node", ID: nodeID}, cfg, credentials, outer)
}

func CompileResourceSOCKS5(resource agentproto.ResourceIdentity, cfg networkconfig.SOCKS5, credentials networkconfig.SOCKS5Credentials, outer networkconfig.ResolvedDirect) (SOCKS5Compilation, error) {
	var out SOCKS5Compilation
	if err := cfg.Validate(); err != nil {
		return out, err
	}
	if err := credentials.Validate(cfg.Authentication); err != nil {
		return out, err
	}
	if !reflect.DeepEqual(cfg.Outer, outer.Config) {
		return out, errors.New("SOCKS5 外层出口与本机解析结果不一致")
	}
	direct, err := CompileResourceDirect(resource, outer)
	if err != nil {
		return out, err
	}
	tag, _ := resource.Tag()
	outTag, bootstrapTag, dnsTag := tag+"-socks5", tag+"-bootstrap", tag+"-dns"
	out.BootstrapDNS = copyFields(direct.DNS)
	out.BootstrapDNS["tag"] = bootstrapTag
	out.Outbound = copyFields(direct.Dial)
	outerStrategy := "prefer_ipv4"
	if cfg.Outer.Family != "dual" {
		outerStrategy = cfg.Outer.Family + "_only"
	}
	out.Outbound["domain_resolver"] = map[string]any{"server": bootstrapTag, "strategy": outerStrategy}
	out.Outbound["type"], out.Outbound["tag"], out.Outbound["version"] = "socks", outTag, "5"
	out.Outbound["server"], out.Outbound["server_port"] = cfg.Server, cfg.ServerPort
	out.Outbound["connect_timeout"] = fmt.Sprintf("%ds", cfg.ConnectTimeoutSeconds)
	out.Outbound["udp_over_tcp"] = false
	if cfg.Authentication == "password" {
		out.Outbound["username"], out.Outbound["password"] = credentials.Username, credentials.Password
	}
	if !cfg.UDP {
		out.Outbound["network"] = "tcp"
		out.UDPReject = map[string]any{"inbound": []string{tag}, "network": "udp", "action": "reject"}
	}
	ip, _ := networkconfig.HostAddress(cfg.DNS.Address)
	out.DNS = map[string]any{"type": cfg.DNS.Transport, "tag": dnsTag, "server": ip.String(), "server_port": cfg.DNS.Port, "detour": outTag}
	strategy := "prefer_ipv4"
	if cfg.Family != "dual" {
		strategy = cfg.Family + "_only"
		blocked := 6
		if cfg.Family == "ipv6" {
			blocked = 4
		}
		out.FamilyReject = map[string]any{"inbound": []string{tag}, "ip_version": blocked, "action": "reject"}
	}
	out.ResolveRule = map[string]any{"inbound": []string{tag}, "action": "resolve", "server": dnsTag, "strategy": strategy}
	return out, nil
}
