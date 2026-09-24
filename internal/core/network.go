package core

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

// DirectCompilation is consumed by the node compiler only after binding and
// guard preflight. It deliberately cannot receive arbitrary sing-box JSON.
type DirectCompilation struct {
	Outbound     map[string]any
	DNS          map[string]any
	ResolveRule  map[string]any
	FamilyReject map[string]any
	Dial         map[string]any // also applied to node-owned Reality handshakes
}

// validateNodeNetwork repeats structural checks at the compiler boundary. The
// agent must still fence traffic and resolve from its local collector first.
func validateNodeNetwork(n agentproto.NodeSpec, ds *agentproto.DesiredState) error {
	if n.Network == nil {
		if n.RuntimeNetwork != nil {
			return errors.New("unexpected local network binding")
		}
		return nil
	}
	if err := n.Network.Validate(); err != nil {
		return err
	}
	if !agentproto.NetworkBindingSupported(n.Core, ds.Versions[agentproto.CoreVersionKey(n.Core)].Version) {
		return errors.New("所选内核版本尚未通过网络绑定验证")
	}
	if n.RuntimeNetwork == nil {
		return errors.New("节点网络尚未由本机解析")
	}
	var direct *networkconfig.Direct
	if n.RuntimeNetwork.Direct != nil {
		direct = &n.RuntimeNetwork.Direct.Config
	}
	wanted := n.Network.OuterBinding()
	if wanted != nil && ds.IPv4Only && wanted.Family == "dual" {
		copy := *wanted
		copy.Family = "ipv4"
		wanted = &copy
	}
	if !reflect.DeepEqual(direct, wanted) {
		return errors.New("本机出口与期望配置不一致")
	}
	if n.Network.HasTransport() {
		transport, _ := n.Network.TransportConfig()
		if !agentproto.SOCKS5BindingSupported(n, ds.Versions["sing-box"].Version) {
			return errors.New("该入口协议与 SOCKS5 出口组合尚未通过验证")
		}
		cfg, err := networkconfig.EffectiveSOCKS5(transport, ds.IPv4Only)
		if err != nil {
			return err
		}
		endpoint := n.RuntimeNetwork.SOCKS5
		ip, err := networkconfig.HostAddress(cfg.Server)
		if endpoint == nil || endpoint.Port != cfg.ServerPort || endpoint.UDP != cfg.UDP || endpoint.Purpose != cfg.Purpose {
			return errors.New("SOCKS5 上游端点与本机保护配置不一致")
		}
		if err == nil {
			if endpoint.Host != "" || endpoint.Address != ip.String() {
				return errors.New("SOCKS5 字面量上游与本机保护配置不一致")
			}
		} else if endpoint.Host != strings.ToLower(strings.TrimSuffix(cfg.Server, ".")) {
			return errors.New("SOCKS5 启动解析主机与配置不一致")
		}
	} else if n.RuntimeNetwork.SOCKS5 != nil {
		return errors.New("直连节点不能携带中转端点权限")
	}
	p := networkguard.Plan{Token: strings.Repeat("0", 32), Bindings: []networkguard.Binding{{NodeID: n.NodeID, ListenPort: n.ListenPort, Core: n.Core, Wanted: n.Network.Policy, Applied: *n.RuntimeNetwork, IPv4Only: ds.IPv4Only}}}
	if IsStandalone(n.Core) {
		if !agentproto.ListenBindingSupported(n, ds.Versions[agentproto.CoreVersionKey(n.Core)].Version) || n.Network.Policy.EgressProfileID != 0 || n.Network.OuterBinding() != nil || n.Network.HasTransport() {
			return errors.New("独立内核仅支持指定监听")
		}
		p.Bindings[0].Group = fmt.Sprintf("/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-n%d.slice", n.NodeID)
	}
	return p.Validate()
}

func CompileDirect(nodeID int64, direct networkconfig.ResolvedDirect) (DirectCompilation, error) {
	return CompileResourceDirect(agentproto.ResourceIdentity{Kind: "node", ID: nodeID}, direct)
}

// CompileResourceDirect is shared by node and forward consumers. Each gets its
// own socket mark, resolver and tags even when their numeric IDs are identical.
func CompileResourceDirect(resource agentproto.ResourceIdentity, direct networkconfig.ResolvedDirect) (DirectCompilation, error) {
	var out DirectCompilation
	if err := direct.Config.Validate(); err != nil {
		return out, err
	}
	mark, err := resource.Mark()
	if err != nil {
		return out, err
	}
	owners := map[string]networkconfig.Interface{}
	indices := map[int]bool{}
	for _, owner := range direct.Owners {
		if !networkconfig.ValidIdentity(owner.ID) || owner.Index < 1 || owner.Name == "" || len(owner.Name) > 15 || strings.ContainsAny(owner.Name, "/:\x00\r\n\t ") || indices[owner.Index] {
			return out, errors.New("已解析的出口接口身份无效")
		}
		if _, exists := owners[owner.ID]; exists {
			return out, errors.New("重复的出口接口身份")
		}
		owners[owner.ID], indices[owner.Index] = owner, true
	}
	if len(owners) > 3 || (direct.Config.InterfaceID == "") != (direct.Interface == nil) {
		return out, errors.New("出口接口尚未完整解析")
	}
	expected := map[string]bool{}
	if direct.Config.InterfaceID != "" {
		expected[direct.Config.InterfaceID] = true
	}
	for _, source := range []*networkconfig.Address{direct.Config.SourceIPv4, direct.Config.SourceIPv6} {
		if source != nil {
			expected[source.InterfaceID] = true
		}
	}
	if len(expected) != len(owners) {
		return out, errors.New("出口接口解析包含无关或缺失的身份")
	}
	dial := map[string]any{"routing_mark": mark}
	if direct.Interface != nil {
		i := *direct.Interface
		if i.ID != direct.Config.InterfaceID || owners[i.ID] != i {
			return out, errors.New("出口引用与本机接口不一致")
		}
		dial["bind_interface"] = i.Name
	}
	for field, source := range map[string]*networkconfig.Address{"inet4_bind_address": direct.Config.SourceIPv4, "inet6_bind_address": direct.Config.SourceIPv6} {
		if source != nil {
			if _, exists := owners[source.InterfaceID]; !exists {
				return out, errors.New("源地址所属接口尚未解析")
			}
			ip, _ := networkconfig.HostAddress(source.Address)
			dial[field] = ip.String()
		}
	}
	tag, _ := resource.Tag()
	dnsTag := fmt.Sprintf("%s-dns", tag)
	strategy := "prefer_ipv4"
	switch direct.Config.Family {
	case "ipv4":
		strategy = "ipv4_only"
		out.FamilyReject = map[string]any{"inbound": []string{tag}, "ip_version": 6, "action": "reject"}
	case "ipv6":
		strategy = "ipv6_only"
		out.FamilyReject = map[string]any{"inbound": []string{tag}, "ip_version": 4, "action": "reject"}
	}
	ip, _ := networkconfig.HostAddress(direct.Config.DNS.Address)
	out.DNS = copyFields(dial)
	out.DNS["type"], out.DNS["tag"] = direct.Config.DNS.Transport, dnsTag
	out.DNS["server"], out.DNS["server_port"] = ip.String(), direct.Config.DNS.Port
	// DNS transports use the direct socket fields rather than detouring through
	// an outbound which needs this resolver itself. No system resolver fallback.
	out.Dial = copyFields(dial)
	out.Dial["domain_resolver"] = map[string]any{"server": dnsTag, "strategy": strategy}
	out.Outbound = copyFields(out.Dial)
	out.Outbound["type"], out.Outbound["tag"] = "direct", tag+"-direct"
	out.ResolveRule = map[string]any{"inbound": []string{tag}, "action": "resolve", "server": dnsTag, "strategy": strategy}
	return out, nil
}

func copyFields(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
