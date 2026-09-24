package networkconfig

import (
	"errors"
	"net/netip"
	"sort"
)

const MaxTransportGrants = 1024
const MaxBindingTransportGrants = 16

// TransportGrant is local root authority, never a controller-writable profile
// field. Node and profile identities must both match; immutable revisions may
// rotate credentials without expanding the permitted transport endpoints.
type TransportGrant struct {
	NodeID          int64  `json:"node_id"`
	EgressProfileID int64  `json:"egress_profile_id"`
	Purpose         string `json:"purpose"` // socks5 | bootstrap_dns
	Network         string `json:"network"` // tcp | udp
	Address         string `json:"address"` // canonical literal IP or masked CIDR
	Port            int    `json:"port"`
	PortEnd         int    `json:"port_end,omitempty"`
}

var grantableTransport = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
}

func NeedsTransportGrant(ip netip.Addr) bool {
	for _, p := range grantableTransport {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func (g TransportGrant) prefix() (netip.Prefix, error) {
	if ip, err := netip.ParseAddr(g.Address); err == nil {
		if ip.Is4In6() || ip.Zone() != "" || ip.String() != g.Address {
			return netip.Prefix{}, errors.New("端点授权 IP 必须使用规范字面量")
		}
		return netip.PrefixFrom(ip, ip.BitLen()), nil
	}
	p, err := netip.ParsePrefix(g.Address)
	if err != nil || p.Addr().Is4In6() || p.Masked() != p || p.String() != g.Address {
		return netip.Prefix{}, errors.New("端点授权网段必须使用规范网络地址")
	}
	return p, nil
}

func (g TransportGrant) LastPort() int {
	if g.PortEnd == 0 {
		return g.Port
	}
	return g.PortEnd
}

func (g TransportGrant) Validate() error {
	if g.NodeID < 1 || g.NodeID > 0x00ffffff || g.EgressProfileID < 1 || g.EgressProfileID > 1<<53-1 {
		return errors.New("端点授权必须指定有效节点和出口 ID")
	}
	if (g.Purpose != "socks5" && g.Purpose != "ssh" && g.Purpose != "wireguard" && g.Purpose != "ss2022" && g.Purpose != "bootstrap_dns") || (g.Network != "tcp" && g.Network != "udp") {
		return errors.New("端点授权用途或传输协议无效")
	}
	if g.Port < 1 || g.Port > 65535 || g.LastPort() < g.Port || g.LastPort() > 65535 || (g.PortEnd != 0 && g.PortEnd == g.Port) {
		return errors.New("端点授权端口范围无效")
	}
	if g.Purpose == "wireguard" && (g.Network != "udp" || g.PortEnd != 0) {
		return errors.New("WireGuard 授权须为固定 UDP 端口")
	}
	if g.Purpose == "ss2022" && g.PortEnd != 0 {
		return errors.New("SS-2022 授权须为固定端口")
	}
	if g.Purpose == "ssh" && (g.Network != "tcp" || g.PortEnd != 0) {
		return errors.New("SSH 传输授权仅接受固定 TCP 端口")
	}
	if g.Purpose == "bootstrap_dns" && g.PortEnd != 0 {
		return errors.New("启动 DNS 授权须指定单个端口")
	}
	p, err := g.prefix()
	if err != nil {
		return err
	}
	for _, private := range grantableTransport {
		if p.Addr().BitLen() == private.Addr().BitLen() && p.Bits() >= private.Bits() && private.Contains(p.Addr()) {
			return nil
		}
	}
	return errors.New("端点窄授权仅接受 RFC1918、共享地址空间或 IPv6 ULA；不能授权本机、链路本地或特殊用途地址")
}

func ValidateTransportGrants(grants []TransportGrant) error {
	if len(grants) > MaxTransportGrants {
		return errors.New("本机传输端点授权数量超限")
	}
	seen := map[TransportGrant]bool{}
	counts := map[[2]int64]int{}
	for _, grant := range grants {
		if err := grant.Validate(); err != nil {
			return err
		}
		if seen[grant] {
			return errors.New("本机传输端点授权重复")
		}
		seen[grant] = true
		key := [2]int64{grant.NodeID, grant.EgressProfileID}
		counts[key]++
		if counts[key] > MaxBindingTransportGrants {
			return errors.New("单个节点出口的端点授权数量超限")
		}
	}
	return nil
}

// Matches assumes the caller has validated the bounded policy, and still
// fails closed on an invalid prefix or mismatched identity/purpose/transport.
func (g TransportGrant) Matches(nodeID, profileID int64, purpose, network string, ip netip.Addr, port int) bool {
	if g.NodeID != nodeID || g.EgressProfileID != profileID || g.Purpose != purpose || g.Network != network || port < g.Port || port > g.LastPort() {
		return false
	}
	p, err := g.prefix()
	return err == nil && p.Contains(ip.Unmap())
}

// TransportGrantsFor returns a canonical independent slice for one binding.
// Stable ordering prevents a reordered local policy from restarting a core.
func TransportGrantsFor(grants []TransportGrant, nodeID, profileID int64) []TransportGrant {
	var out []TransportGrant
	for _, g := range grants {
		if g.NodeID == nodeID && g.EgressProfileID == profileID {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Purpose != b.Purpose {
			return a.Purpose < b.Purpose
		}
		if a.Network != b.Network {
			return a.Network < b.Network
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.LastPort() < b.LastPort()
	})
	return out
}

// AuthorizeTransport checks only the root exception. Structural and current
// local-address validation remain mandatory even for an authorized endpoint.
func AuthorizeTransport(grants []TransportGrant, nodeID, profileID int64, purpose, network string, ip netip.Addr, port int) bool {
	if ValidateTransportGrants(grants) != nil || (network != "tcp" && network != "udp") || port < 1 || port > 65535 {
		return false
	}
	for _, grant := range grants {
		if grant.Matches(nodeID, profileID, purpose, network, ip, port) {
			return true
		}
	}
	return false
}
