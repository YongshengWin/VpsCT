package networkconfig

import (
	"errors"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// ResolvedSOCKS5 is an agent-local transport boundary. The core and firewall
// consume the same address. UDP relay sockets may use dynamic ports, but never
// a different address; a separate relay range requires a future explicit grant.
type ResolvedSOCKS5 struct {
	Purpose string `json:",omitempty"` // empty = legacy SOCKS5, ssh = SSH transport
	Host    string // empty for a literal endpoint; otherwise the queried hostname
	Address string
	Port    int
	UDP     bool
	// Matched local root grants. These are never accepted from desired JSON.
	TransportGrants []TransportGrant
	ResolvedAt      time.Time
	RefreshAt       time.Time
	ExpiresAt       time.Time
}

// Keep lookup/reconfiguration load bounded for zero or very short TTLs, and
// never pin an endpoint for more than five minutes without another lookup.
func (r *ResolvedSOCKS5) SetDNSLifetime(now time.Time, ttl uint32) {
	lifetime := time.Duration(max(uint32(30), min(ttl, uint32(300)))) * time.Second
	r.ResolvedAt = now.UTC()
	r.RefreshAt = r.ResolvedAt.Add(lifetime / 2)
	r.ExpiresAt = r.ResolvedAt.Add(lifetime)
}

func (r ResolvedSOCKS5) DNSRefreshDue(now time.Time) bool {
	return r.Host != "" && (now.Before(r.ResolvedAt) || !now.Before(r.RefreshAt))
}

func (r ResolvedSOCKS5) DNSExpired(now time.Time) bool {
	return r.Host != "" && (now.Before(r.ResolvedAt) || !now.Before(r.ExpiresAt))
}

func (r ResolvedSOCKS5) WithoutDNSLifetime() ResolvedSOCKS5 {
	r.ResolvedAt, r.RefreshAt, r.ExpiresAt = time.Time{}, time.Time{}, time.Time{}
	return r
}

func (r ResolvedSOCKS5) Validate() error {
	if r.Purpose != "" && r.Purpose != "ssh" && r.Purpose != "wireguard" && r.Purpose != "ss2022" || r.Purpose == "ssh" && r.UDP || (r.Purpose == "wireguard" || r.Purpose == "ss2022") && !r.UDP {
		return errors.New("传输端点用途无效")
	}
	if err := r.validateDNS(); err != nil {
		return err
	}
	ip, err := HostAddress(r.Address)
	if err != nil || r.Port < 1 || r.Port > 65535 {
		return errors.New("SOCKS5 上游端点未完整解析")
	}
	if NeedsTransportGrant(ip) {
		if err := ValidateTransportGrants(r.TransportGrants); err != nil || len(r.TransportGrants) == 0 {
			return errors.New("SOCKS5 私网端点需要独立本机授权，不能继承业务私网权限")
		}
		var tcp, udp bool
		owner := r.TransportGrants[0]
		for _, grant := range r.TransportGrants {
			if grant.NodeID != owner.NodeID || grant.EgressProfileID != owner.EgressProfileID || !grant.Matches(owner.NodeID, owner.EgressProfileID, r.TransportPurpose(), grant.Network, ip, grant.Port) {
				return errors.New("SOCKS5 端点授权身份或范围不匹配")
			}
			if grant.Network == "tcp" {
				if !r.TCPAllowed() {
					return errors.New("WireGuard 端点不能使用 TCP 授权")
				}
				if r.Port < grant.Port || r.Port > grant.LastPort() {
					return errors.New("SOCKS5 TCP 端口不在授权范围")
				}
				tcp = true
			} else {
				if !r.UDP || (r.Purpose == "wireguard" || r.Purpose == "ss2022") && (grant.Port != r.Port || grant.PortEnd != 0) {
					return errors.New("仅 TCP 端点不能携带 UDP 授权")
				}
				udp = true
			}
		}
		if (r.TCPAllowed() && !tcp) || (r.UDP && !udp) {
			return errors.New("SOCKS5 缺少对应 TCP 或 UDP 传输授权")
		}
		return nil
	}
	if len(r.TransportGrants) != 0 {
		return errors.New("公网端点不能携带私网传输授权")
	}
	// Match the transport restrictions in the general proxy firewall. This
	// includes translation/metadata ranges that IsPrivate alone does not cover.
	for _, prefix := range transportRestricted {
		if prefix.Contains(ip) {
			return errors.New("SOCKS5 上游地址不符合本机端点策略")
		}
	}
	return nil
}

// WithTransportGrants attaches only matching local authority, without changing
// business-private permission or accepting a grant for another binding.
func (r ResolvedSOCKS5) WithTransportGrants(nodeID, profileID int64, grants []TransportGrant) (ResolvedSOCKS5, error) {
	r.TransportGrants = nil
	ip, err := HostAddress(r.Address)
	if err != nil {
		return r, err
	}
	if err := ValidateTransportGrants(grants); err != nil {
		return r, err
	}
	if NeedsTransportGrant(ip) {
		for _, grant := range TransportGrantsFor(grants, nodeID, profileID) {
			port := r.Port
			if grant.Network == "udp" && r.Purpose != "wireguard" && r.Purpose != "ss2022" {
				port = grant.Port
			}
			if (grant.Network == "tcp" && r.TCPAllowed() || grant.Network == "udp" && r.UDP) && grant.Matches(nodeID, profileID, r.TransportPurpose(), grant.Network, ip, port) {
				r.TransportGrants = append(r.TransportGrants, grant)
			}
		}
	}
	return r, r.Validate()
}

func (r ResolvedSOCKS5) TCPAllowed() bool { return r.Purpose != "wireguard" }

func (r ResolvedSOCKS5) TransportPurpose() string {
	if r.Purpose == "ssh" || r.Purpose == "wireguard" || r.Purpose == "ss2022" {
		return r.Purpose
	}
	return "socks5"
}

func (r ResolvedSOCKS5) ValidateForBinding(nodeID, profileID int64) error {
	if err := r.Validate(); err != nil {
		return err
	}
	for _, grant := range r.TransportGrants {
		if grant.NodeID != nodeID || grant.EgressProfileID != profileID {
			return errors.New("SOCKS5 端点使用了其他节点或出口的本机授权")
		}
	}
	return nil
}

// StillAuthorized requires every applied exception to remain explicitly
// present. A changed range is reapplied rather than extending old leases.
func (r ResolvedSOCKS5) StillAuthorized(current []TransportGrant) bool {
	if r.Validate() != nil || ValidateTransportGrants(current) != nil {
		return false
	}
	for _, applied := range r.TransportGrants {
		found := false
		for _, grant := range current {
			if grant == applied {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type TransportPortRange struct{ First, Last int }

// UDPPorts is the local authorization intersection for the pinned IP. SOCKS5
// chooses its relay port at association time; a port outside this set fails.
func (r ResolvedSOCKS5) UDPPorts() []TransportPortRange {
	if !r.UDP || r.Validate() != nil {
		return nil
	}
	if r.Purpose == "wireguard" || r.Purpose == "ss2022" {
		return []TransportPortRange{{r.Port, r.Port}}
	}
	if len(r.TransportGrants) == 0 {
		return []TransportPortRange{{1, 65535}}
	}
	var ranges []TransportPortRange
	for _, grant := range r.TransportGrants {
		if grant.Network == "udp" {
			ranges = append(ranges, TransportPortRange{grant.Port, grant.LastPort()})
		}
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].First < ranges[j].First })
	var merged []TransportPortRange
	for _, ports := range ranges {
		if len(merged) > 0 && ports.First <= merged[len(merged)-1].Last+1 {
			merged[len(merged)-1].Last = max(merged[len(merged)-1].Last, ports.Last)
		} else {
			merged = append(merged, ports)
		}
	}
	return merged
}

var transportRestricted = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fe80::/10"),
}

// EffectiveSOCKS5 tightens both business and outer paths under IPv4Only.
// It never mutates the immutable controller configuration.
func EffectiveSOCKS5(cfg SOCKS5, ipv4Only bool) (SOCKS5, error) {
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	if ipv4Only {
		if cfg.Family == "ipv6" || cfg.Outer.Family == "ipv6" {
			return cfg, errors.New("服务器仅 IPv4 设置与 SOCKS5 地址族冲突")
		}
		cfg.Family, cfg.Outer.Family = "ipv4", "ipv4"
	}
	return cfg, cfg.Validate()
}

func (r ResolvedSOCKS5) validateDNS() error {
	if r.Host != "" {
		if ValidateAdvertiseHost(r.Host) != nil || strings.ToLower(strings.TrimSuffix(r.Host, ".")) != r.Host {
			return errors.New("SOCKS5 启动解析主机无效")
		}
		if _, err := netip.ParseAddr(r.Host); err == nil {
			return errors.New("SOCKS5 启动解析主机不能是字面量地址")
		}
		lifetime := r.ExpiresAt.Sub(r.ResolvedAt)
		if r.ResolvedAt.IsZero() || lifetime < 30*time.Second || lifetime > 5*time.Minute || !r.RefreshAt.Equal(r.ResolvedAt.Add(lifetime/2)) {
			return errors.New("SOCKS5 域名端点刷新期限无效")
		}
	} else if !r.ResolvedAt.IsZero() || !r.RefreshAt.IsZero() || !r.ExpiresAt.IsZero() {
		return errors.New("字面量端点不能携带 DNS 刷新期限")
	}
	return nil
}
