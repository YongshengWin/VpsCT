package agentproto

import (
	"errors"
	"strings"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/networkconfig"
)

const NetworkBindingVersion = networkconfig.BindingVersion

// Each desired fetch must declare support. Cached heartbeat metadata is not
// sufficient: the executable may have been replaced since the last heartbeat.
const NetworkBindingHeader = networkconfig.BindingHeader

const NetworkEgressVersion = networkconfig.EgressVersion
const NetworkEgressHeader = networkconfig.EgressHeader
const NetworkWireGuardVersion = networkconfig.WireGuardVersion
const NetworkWireGuardHeader = networkconfig.WireGuardHeader
const NetworkSSHVersion = networkconfig.SSHVersion
const NetworkSSHHeader = networkconfig.SSHHeader

// Only diagnostics report local authority. Desired configuration never grants it.
const NetworkTransportVersion = 1

func ValidateNetworkDiagnostics(d Diagnostics) error {
	if d.ForwardPrivateVersion < 0 || d.ForwardPrivateVersion > 64 || len(d.ForwardGrants) > 0 && d.ForwardPrivateVersion != 1 {
		return errors.New("私网转发能力诊断无效")
	}
	if err := networkconfig.ValidateForwardGrants(d.ForwardGrants); err != nil {
		return err
	}
	if d.MeterInventoryVersion < 0 || d.MeterInventoryVersion > 1 || len(d.RetainedNodeMeters) > 2048 || (len(d.RetainedNodeMeters) > 0 && d.MeterInventoryVersion != 1) {
		return errors.New("计量清理诊断无效")
	}
	seenMeters := map[int64]bool{}
	for _, id := range d.RetainedNodeMeters {
		if id < 1 || id > 1<<53-1 || seenMeters[id] {
			return errors.New("计量清理身份无效")
		}
		seenMeters[id] = true
	}
	if d.ForwardDNSVersion < 0 || d.ForwardDNSVersion > 64 {
		return errors.New("固定转发解析能力无效")
	}
	if d.ForwardTransportVersion < 0 || d.ForwardTransportVersion > 64 {
		return errors.New("固定转发中转能力无效")
	}
	if d.ListenBindingVersion < 0 || d.ListenBindingVersion > 64 {
		return errors.New("独立监听能力版本无效")
	}
	if d.MitaVersion < 0 || d.MitaVersion > 64 {
		return errors.New("mita 能力版本无效")
	}
	if d.NetworkWireGuardVersion < 0 || d.NetworkWireGuardVersion > 64 {
		return errors.New("WireGuard 能力版本无效")
	}
	if d.NetworkSSHVersion < 0 || d.NetworkSSHVersion > 64 {
		return errors.New("SSH 中转能力版本无效")
	}
	if len(d.NetworkForwardErrors) > agentbudget.ActiveForwards {
		return errors.New("固定转发诊断超出限额")
	}
	for id, detail := range d.NetworkForwardErrors {
		if id < 1 || id > 0xffffff || len(detail) > 512 {
			return errors.New("固定转发诊断字段无效")
		}
	}
	if d.NetworkBindingVersion < 0 || d.NetworkBindingVersion > 64 || d.NetworkEgressVersion < 0 || d.NetworkEgressVersion > 64 || d.NetworkForwardVersion < 0 || d.NetworkForwardVersion > 64 || d.NetworkTransportVersion < 0 || d.NetworkTransportVersion > 64 || len(d.NetworkGuardError) > 512 || len(d.NetworkBindingErrors) > 2048 {
		return errors.New("网络绑定诊断超出限额")
	}
	for id, detail := range d.NetworkBindingErrors {
		if id < 1 || id > 1<<53-1 || len(detail) > 512 {
			return errors.New("网络绑定诊断字段无效")
		}
	}
	if len(d.TransportGrants) > 0 && d.NetworkTransportVersion != NetworkTransportVersion {
		return errors.New("端点授权诊断缺少兼容能力版本")
	}
	if err := networkconfig.ValidateTransportGrants(d.TransportGrants); err != nil {
		return err
	}
	return nil
}

// NodeNetworkSpec carries an immutable profile revision and its structured
// configuration. No remote interface name, ifindex or runtime observation is
// accepted. Omission retains the legacy wire representation and content hash.
type NodeNetworkSpec struct {
	Policy    networkconfig.Node    `json:"policy"`
	Direct    *networkconfig.Direct `json:"direct,omitempty"`
	SOCKS5    *SOCKS5Egress         `json:"socks5,omitempty"`
	SS2022    *SS2022Egress         `json:"ss2022,omitempty"`
	WireGuard *WireGuardEgress      `json:"wireguard,omitempty"`
	SSH       *SSHEgress            `json:"ssh,omitempty"`
}

type WireGuardEgress struct {
	Config      networkconfig.WireGuard         `json:"config"`
	Credentials networkconfig.SOCKS5Credentials `json:"credentials"`
}

type SSHEgress struct {
	Config      networkconfig.SSH               `json:"config"`
	Credentials networkconfig.SOCKS5Credentials `json:"credentials"`
}

// TransportConfig shares endpoint resolution and fencing, never authentication
// or the wire identity. Old agents reject SSH instead of interpreting it as SOCKS.
func (n NodeNetworkSpec) TransportConfig() (networkconfig.SOCKS5, bool) {
	if n.SS2022 != nil {
		return n.SS2022.Config.Transport(), true
	}
	if n.WireGuard != nil {
		return n.WireGuard.Config.Transport(), true
	}
	if n.SSH != nil {
		return n.SSH.Config.Transport(), true
	}
	if n.SOCKS5 != nil {
		return n.SOCKS5.Config, true
	}
	return networkconfig.SOCKS5{}, false
}

func (n NodeNetworkSpec) HasTransport() bool {
	return n.SOCKS5 != nil || n.SSH != nil || n.WireGuard != nil || n.SS2022 != nil
}

// This type belongs only to the authenticated agent payload, never public
// profile responses, client subscriptions or network-operation receipts.
type SOCKS5Egress struct {
	Config      networkconfig.SOCKS5            `json:"config"`
	Credentials networkconfig.SOCKS5Credentials `json:"credentials"`
}

type SS2022Egress struct {
	Config      networkconfig.SS2022            `json:"config"`
	Credentials networkconfig.SOCKS5Credentials `json:"credentials"`
}

func (n NodeNetworkSpec) OuterBinding() *networkconfig.Direct {
	if n.SS2022 != nil {
		return &n.SS2022.Config.Outer
	}
	if n.WireGuard != nil {
		return &n.WireGuard.Config.Outer
	}
	if n.SSH != nil {
		return &n.SSH.Config.Outer
	}
	if n.SOCKS5 != nil {
		return &n.SOCKS5.Config.Outer
	}
	return n.Direct
}

func (n NodeNetworkSpec) Validate() error {
	if err := n.Policy.Validate(); err != nil {
		return err
	}
	count := 0
	if n.WireGuard != nil {
		count++
	}
	if n.Direct != nil {
		count++
	}
	if n.SOCKS5 != nil {
		count++
	}
	if n.SSH != nil {
		count++
	}
	if n.SS2022 != nil {
		count++
	}
	if count > 1 {
		return errors.New("每个节点只能指定一种出口配置")
	}
	if (n.Policy.EgressProfileID == 0) != (count == 0) {
		return errors.New("出口版本与配置必须同时提供")
	}
	if n.WireGuard != nil {
		return n.WireGuard.Credentials.ValidateWireGuard(n.WireGuard.Config)
	}
	if n.Direct != nil {
		return n.Direct.Validate()
	}
	if n.SSH != nil {
		if err := n.SSH.Config.Validate(); err != nil {
			return err
		}
		return n.SSH.Credentials.ValidateSSH(n.SSH.Config)
	}
	if n.SOCKS5 != nil {
		if err := n.SOCKS5.Config.Validate(); err != nil {
			return err
		}
		return n.SOCKS5.Credentials.Validate(n.SOCKS5.Config.Authentication)
	}
	if n.SS2022 != nil {
		if err := n.SS2022.Config.Validate(); err != nil {
			return err
		}
		return n.SS2022.Credentials.ValidateSS2022(n.SS2022.Config.Method)
	}
	return nil
}

// NetworkBindingSupported is deliberately an allowlist of verified core
// versions. Choosing a new feature never silently upgrades the installed core.
func NetworkBindingSupported(core, version string) bool {
	if core == "singbox" {
		return corecompat.NetworkBinding(version)
	}
	want := map[string]string{"snell": "5.0.1", "mieru": "3.37.0"}
	return want[core] != "" && strings.TrimPrefix(version, "v") == want[core]
}

// Direct inbounds share a marked dial path; Reality also binds its handshake.
func DirectBindingProtocols() []string {
	return []string{"ss", "vless", "trojan", "anytls", "hysteria2", "tuic"}
}
func DirectBindingSupported(n NodeSpec, version string) bool {
	if n.Core != "singbox" || !NetworkBindingSupported(n.Core, version) {
		return false
	}
	switch n.Protocol {
	case "vless", "trojan", "anytls", "hysteria2", "tuic":
		return true
	default:
		return SOCKS5BindingSupported(n, version)
	}
}

// Extend this admission list only with production inbound/transport evidence.
// In particular, Reality's separate handshake path also needs scoped DNS and
// destination checks; a working SS inbound must not implicitly qualify it.
func SOCKS5BindingSupported(n NodeSpec, version string) bool {
	if !NetworkBindingSupported(n.Core, version) || (n.Protocol != "ss" && n.Protocol != "shadowsocks") {
		return false
	}
	method, ok := n.Params["method"]
	return !ok || method == "" || method == "2022-blake3-aes-128-gcm"
}

// Standalone cores expose only listening selection, never a custom dial path.
func ListenBindingSupported(n NodeSpec, version string) bool {
	return (n.Core == "snell" && n.Protocol == "snell" || n.Core == "mieru" && n.Protocol == "mieru") && NetworkBindingSupported(n.Core, version)
}
func CoreVersionKey(core string) string {
	return map[string]string{"singbox": "sing-box", "snell": "snell-server", "mieru": "mita"}[core]
}
