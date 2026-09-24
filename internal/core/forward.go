package core

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

type ForwardCompilation struct {
	Inbound, Outbound, Route map[string]any
	TransportOutbound        map[string]any
	DNS                      []any
	Rules                    []any
	Endpoint                 bool
}

// CompileForward consumes a locally resolved candidate. No DNS is emitted:
// the same literal target is checked by the root guard and used by the core.
func CompileForward(f agentproto.ForwardSpec, ds *agentproto.DesiredState) (ForwardCompilation, error) {
	var result ForwardCompilation
	if err := f.Validate(); err != nil {
		return result, err
	}
	if f.Blocked || f.Retired {
		return result, errors.New("停用或退役转发不能生成监听")
	}
	if ds == nil || !agentproto.NetworkBindingSupported("singbox", ds.Versions["sing-box"].Version) {
		return result, errors.New("所选内核版本尚未通过网络绑定验证")
	}
	if f.Config.Network != "tcp" && !corecompat.UDPForward(ds.Versions["sing-box"].Version) {
		return result, errors.New(corecompat.UDPForwardUnavailable)
	}
	if f.RuntimeNetwork == nil {
		return result, errors.New("固定转发网络尚未由本机解析")
	}
	ip, err := f.Config.ResolvedTarget(f.RuntimeNetwork.ForwardTarget)
	if err != nil {
		return result, err
	}
	wanted := f.OuterBinding()
	if wanted != nil && ds.IPv4Only && wanted.Family == "dual" {
		copy := *wanted
		copy.Family = "ipv4"
		wanted = &copy
	}
	var applied *networkconfig.Direct
	if f.RuntimeNetwork.Direct != nil {
		applied = &f.RuntimeNetwork.Direct.Config
	}
	if !reflect.DeepEqual(wanted, applied) {
		return result, errors.New("固定转发本机出口与期望配置不一致")
	}
	family := ""
	if transport, ok := f.TransportConfig(); ok {
		family = transport.Family
	}
	plan := networkguard.Plan{Token: strings.Repeat("0", 32), Bindings: []networkguard.Binding{{
		ForwardID: f.ForwardID, Forward: &f.Config, ListenPort: f.Config.ListenPort, Core: "singbox",
		Wanted: f.Config.BindingPolicy(), Applied: *f.RuntimeNetwork, IPv4Only: ds.IPv4Only, ForwardTransport: f.HasTransport(), ForwardFamily: family,
	}}}
	if err := plan.Validate(); err != nil {
		return result, err
	}
	resource := agentproto.ResourceIdentity{Kind: "forward", ID: f.ForwardID}
	mark, _ := resource.Mark()
	tag, _ := resource.Tag()
	result.Inbound = map[string]any{"type": "direct", "tag": tag, "listen": f.RuntimeNetwork.ListenAddress,
		"listen_port": f.Config.ListenPort, "override_address": ip.String(), "override_port": f.Config.TargetPort}
	if f.Config.Network == "both" {
		result.Inbound["network"] = []string{"tcp", "udp"}
	} else {
		result.Inbound["network"] = f.Config.Network
	}
	if f.Config.Network != "tcp" {
		result.Inbound["udp_timeout"] = fmt.Sprintf("%ds", f.Config.UDPIdleSeconds)
	}
	result.Outbound = map[string]any{"type": "direct", "tag": tag + "-direct", "routing_mark": mark}
	if f.HasTransport() {
		cfg, _ := f.TransportConfig()
		if f.Config.Network != "tcp" && (f.SSH != nil || !cfg.UDP) {
			return result, errors.New("UDP 固定转发需要支持 UDP 的中转出口")
		}
		cfg, err = networkconfig.EffectiveSOCKS5(cfg, ds.IPv4Only)
		if err != nil {
			return result, err
		}
		endpoint := f.RuntimeNetwork.SOCKS5
		if endpoint == nil || f.RuntimeNetwork.Direct == nil || endpoint.Port != cfg.ServerPort || endpoint.Purpose != cfg.Purpose || endpoint.UDP != cfg.UDP {
			return result, errors.New("固定转发中转端点与本机保护配置不一致")
		}
		configuredIP, err := networkconfig.HostAddress(cfg.Server)
		if err != nil || configuredIP.String() != endpoint.Address || endpoint.Host != "" || len(endpoint.TransportGrants) != 0 {
			return result, errors.New("固定转发中转须使用独立核验的公网字面量端点")
		}
		cfg.Server = endpoint.Address
		var compiled SOCKS5Compilation
		if f.WireGuard != nil {
			compiled, err = CompileResourceWireGuard(resource, f.WireGuard.Config, f.WireGuard.Credentials, cfg, *f.RuntimeNetwork.Direct)
		} else if f.SSH != nil {
			compiled, err = CompileResourceSSH(resource, f.SSH.Config, f.SSH.Credentials, cfg, *f.RuntimeNetwork.Direct)
		} else if f.SS2022 != nil {
			if !corecompat.SS2022Outbound(ds.Versions["sing-box"].Version) {
				return result, errors.New(corecompat.SS2022Requirement)
			}
			compiled, err = CompileResourceSS2022(resource, f.SS2022.Config, f.SS2022.Credentials, cfg, *f.RuntimeNetwork.Direct)
		} else {
			compiled, err = CompileResourceSOCKS5(resource, cfg, f.SOCKS5.Credentials, *f.RuntimeNetwork.Direct)
		}
		if err != nil {
			return result, err
		}
		result.Outbound, result.TransportOutbound = compiled.Outbound, compiled.TransportOutbound
		result.Endpoint = f.WireGuard != nil
		result.DNS = []any{compiled.BootstrapDNS, compiled.DNS}
		if compiled.UDPReject != nil {
			result.Rules = append(result.Rules, compiled.UDPReject)
		}
		if compiled.FamilyReject != nil {
			result.Rules = append(result.Rules, compiled.FamilyReject)
		}
		result.Route = map[string]any{"inbound": []string{tag}, "action": "route", "outbound": compiled.Outbound["tag"]}
		if f.Config.Network != "tcp" {
			result.Route["udp_timeout"] = fmt.Sprintf("%ds", f.Config.UDPIdleSeconds)
		}
		return result, nil
	}
	if f.RuntimeNetwork.Direct != nil {
		compiled, err := CompileResourceDirect(resource, *f.RuntimeNetwork.Direct)
		if err != nil {
			return result, err
		}
		result.Outbound = compiled.Outbound
		// Fixed literal forwarding has no resolver or domain fallback. Retain
		// only socket/interface/source fields from the common dial compiler.
		delete(result.Outbound, "domain_resolver")
	}
	result.Route = map[string]any{"inbound": []string{tag}, "action": "route", "outbound": tag + "-direct"}
	if f.Config.Network != "tcp" {
		result.Route["udp_timeout"] = fmt.Sprintf("%ds", f.Config.UDPIdleSeconds)
	}
	return result, nil
}

// BuildResourceConfig is the single shared-process configuration compiler.
// The caller supplies only candidates whose root fence was installed. Desired
// forwards are never implicitly read by the legacy node-only BuildConfig API.
func (d *SingBox) BuildResourceConfig(ds *agentproto.DesiredState, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) (map[string]any, error) {
	if len(forwards) > agentbudget.ActiveForwards {
		return nil, errors.New("固定转发数量超过本机预算")
	}
	ports := map[int]bool{}
	for _, n := range nodes {
		if ports[n.ListenPort] {
			return nil, errors.New("共享进程监听端口冲突")
		}
		ports[n.ListenPort] = true
	}
	ids := map[int64]bool{}
	ordered := append([]agentproto.ForwardSpec(nil), forwards...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ForwardID < ordered[j].ForwardID })
	var compiled []ForwardCompilation
	for _, f := range ordered {
		if err := f.Validate(); err != nil {
			return nil, err
		}
		if ids[f.ForwardID] {
			return nil, errors.New("重复的固定转发身份")
		}
		ids[f.ForwardID] = true
		if f.Blocked || f.Retired {
			continue
		}
		if ports[f.Config.ListenPort] {
			return nil, errors.New("节点与固定转发监听端口冲突")
		}
		ports[f.Config.ListenPort] = true
		c, err := CompileForward(f, ds)
		if err != nil {
			return nil, fmt.Errorf("forward %d: %w", f.ForwardID, err)
		}
		compiled = append(compiled, c)
	}
	cfg, err := d.BuildConfig(ds, nodes)
	if err != nil {
		return nil, err
	}
	route := cfg["route"].(map[string]any)
	for _, c := range compiled {
		cfg["inbounds"] = append(cfg["inbounds"].([]any), c.Inbound)
		if c.TransportOutbound != nil {
			cfg["outbounds"] = append(cfg["outbounds"].([]any), c.TransportOutbound)
		}
		if c.Endpoint {
			cfg["endpoints"] = appendEndpoints(cfg["endpoints"], c.Outbound)
		} else {
			cfg["outbounds"] = append(cfg["outbounds"].([]any), c.Outbound)
		}
		cfg["dns"].(map[string]any)["servers"] = append(cfg["dns"].(map[string]any)["servers"].([]any), c.DNS...)
		if len(c.DNS) > 0 && !corecompat.ModernConfig(ds.Versions["sing-box"].Version) {
			cfg["dns"].(map[string]any)["independent_cache"] = true
		}
		route["rules"] = append(route["rules"].([]any), c.Rules...)
		route["rules"] = append(route["rules"].([]any), c.Route)
	}
	return cfg, nil
}

func appendEndpoints(existing any, endpoint map[string]any) []any {
	if existing == nil {
		return []any{endpoint}
	}
	return append(existing.([]any), endpoint)
}
