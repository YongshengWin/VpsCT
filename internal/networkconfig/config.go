// Package networkconfig defines server-side network policies. These values are
// separate from client protocol parameters and contain no exported credentials.
package networkconfig

import (
	"errors"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Shared by the protocol and the isolated HTTP helper without introducing a
// dependency from the low-privilege transport to maintenance/control types.
const BindingVersion = 1
const BindingHeader = "X-Ctlvps-Network-Binding-Version"
const IngressVersion = 1
const IngressHeader = "X-Ctlvps-Network-Ingress-Version"
const IPv6IngressVersion = 1
const IPv6IngressHeader = "X-Ctlvps-IPv6-Ingress-Version"

// Transit has its own contract: understanding direct bindings does not imply
// understanding upstream transports. Keep direct-only payloads unchanged.
const EgressVersion = 1
const EgressHeader = "X-Ctlvps-Network-Egress-Version"

// Node is persisted with a node or an automatic share-deployment target. A nil
// policy preserves legacy behavior; an explicit policy always fails closed.
type Node struct {
	IngressFamily     string         `json:"ingress_family,omitempty"` // IPv6 nodes use ipv6
	ListenMode        string         `json:"listen_mode"`              // all | address | public_ipv6
	ListenAddress     string         `json:"listen_address,omitempty"`
	ListenInterfaceID string         `json:"listen_interface_id,omitempty"`
	AdvertiseMode     string         `json:"advertise_mode"` // inherit | override | inherit_ipv6
	EgressProfileID   int64          `json:"egress_profile_id,omitempty"`
	EgressRevision    int64          `json:"egress_revision,omitempty"`
	OnUnavailable     string         `json:"on_unavailable"` // block
	Ingress           *IngressPolicy `json:"ingress,omitempty"`
}

// IngressPolicy controls who may open the node listener. It is evaluated by
// the agent's network guard before the general port allow rule. GeoIP fields
// are materialized by the controller from a pinned country database; an
// agent never performs online IP lookups.
type IngressPolicy struct {
	Mode         string   `json:"mode"` // relay_only | deny_countries
	SourceCIDRs  []string `json:"source_cidrs,omitempty"`
	Countries    []string `json:"countries,omitempty"`
	GeoIPv4CIDRs []string `json:"geo_ipv4_cidrs,omitempty"`
	GeoIPv6CIDRs []string `json:"geo_ipv6_cidrs,omitempty"`
	GeoProvider  string   `json:"geo_provider,omitempty"`
	GeoVersion   string   `json:"geo_version,omitempty"`
}

// Address retains its owner even when it is used without SO_BINDTODEVICE. An
// IP moving to a different interface must not silently inherit this binding.
type Address struct {
	InterfaceID string `json:"interface_id"`
	Address     string `json:"address"`
}

// Resolver uses a literal IP to avoid recursively resolving the DNS endpoint.
// Each consumer compiles its own transport and cache, with its own traffic mark.
type Resolver struct {
	Transport string `json:"transport"` // udp | tcp
	Address   string `json:"address"`
	Port      int    `json:"port"`
}

type Direct struct {
	InterfaceID string   `json:"interface_id,omitempty"`
	SourceIPv4  *Address `json:"source_ipv4,omitempty"`
	SourceIPv6  *Address `json:"source_ipv6,omitempty"`
	Family      string   `json:"family"` // dual | ipv4 | ipv6
	DNS         Resolver `json:"dns"`
}

var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)

func ValidIdentity(id string) bool { return identity.MatchString(id) }

// HostAddress excludes wildcard, mapped, scoped, loopback and multicast forms.
// Private addresses remain representable; local permission and routing checks
// are separate from syntax and must run before activation.
func HostAddress(raw string) (netip.Addr, error) {
	a, err := netip.ParseAddr(raw)
	if err != nil || a.Is4In6() || a.Zone() != "" || !a.IsGlobalUnicast() {
		return netip.Addr{}, errors.New("须使用有效的单播 IPv4/IPv6 地址，不能使用通配、回环或带作用域的地址")
	}
	return a, nil
}

func (n Node) Validate() error {
	if n.IngressFamily != "" && n.IngressFamily != "default" && n.IngressFamily != "ipv6" {
		return errors.New("节点接入地址族无效")
	}
	if n.IngressFamily == "ipv6" {
		if n.AdvertiseMode != "inherit_ipv6" && n.AdvertiseMode != "override" {
			return errors.New("仅 IPv6 节点需要独立 IPv6 访问地址")
		}
		if n.ListenMode == "all" || n.ListenMode == "address" && !IsPublicIPv6(n.ListenAddress) {
			return errors.New("仅 IPv6 节点必须绑定公网 IPv6")
		}
	} else if n.AdvertiseMode != "inherit" && n.AdvertiseMode != "override" {
		return errors.New("节点访问地址模式无效")
	}
	if n.OnUnavailable != "block" {
		return errors.New("网络不可用时必须阻断，不能回退到其他出口")
	}
	switch n.ListenMode {
	case "all":
		if n.ListenAddress != "" || n.ListenInterfaceID != "" {
			return errors.New("全部地址监听不能同时指定地址或接口")
		}
	case "address":
		if !ValidIdentity(n.ListenInterfaceID) {
			return errors.New("监听地址缺少接口身份")
		}
		if _, err := HostAddress(n.ListenAddress); err != nil {
			return err
		}
	case "public_ipv6":
		if n.IngressFamily != "ipv6" || n.ListenAddress != "" || n.ListenInterfaceID != "" && !ValidIdentity(n.ListenInterfaceID) {
			return errors.New("自动 IPv6 监听参数无效")
		}
	default:
		return errors.New("监听模式无效")
	}
	if n.EgressProfileID < 0 || n.EgressRevision < 0 || n.EgressProfileID > 1<<53-1 || n.EgressRevision > 1<<53-1 || (n.EgressProfileID == 0) != (n.EgressRevision == 0) {
		return errors.New("出口配置和不可变版本必须同时指定")
	}
	if err := n.Ingress.Validate(); err != nil {
		return err
	}
	return nil
}

const (
	IngressRelayOnly     = "relay_only"
	IngressDenyCountries = "deny_countries"
	GeoIPProviderDBIP    = "dbip-country-lite"
	GeoIPProviderMaxMind = "maxmind-geolite2-country"
)

func (p *IngressPolicy) Validate() error {
	if p == nil {
		return nil
	}
	switch p.Mode {
	case IngressRelayOnly:
		if len(p.SourceCIDRs) == 0 || len(p.SourceCIDRs) > 256 {
			return errors.New("仅允许中转模式必须包含 1–256 个来源网段")
		}
		if len(p.Countries) != 0 || len(p.GeoIPv4CIDRs) != 0 || len(p.GeoIPv6CIDRs) != 0 || p.GeoProvider != "" || p.GeoVersion != "" {
			return errors.New("仅允许中转模式不能携带 GeoIP 数据")
		}
	case IngressDenyCountries:
		if len(p.Countries) == 0 || len(p.Countries) > 64 {
			return errors.New("地区拒绝模式必须包含 1–64 个国家代码")
		}
		seen := map[string]bool{}
		for i, country := range p.Countries {
			country = strings.ToUpper(strings.TrimSpace(country))
			if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' || seen[country] {
				return errors.New("国家代码必须是不重复的 ISO 3166-1 两位大写代码")
			}
			p.Countries[i] = country
			seen[country] = true
		}
		if (p.GeoProvider != GeoIPProviderDBIP && p.GeoProvider != GeoIPProviderMaxMind) || strings.TrimSpace(p.GeoVersion) == "" {
			return errors.New("地区拒绝模式缺少已固定版本的 GeoIP 数据源")
		}
		if len(p.GeoIPv4CIDRs) == 0 && len(p.GeoIPv6CIDRs) == 0 {
			return errors.New("地区拒绝模式缺少已物化的 IPv4/IPv6 地址集合")
		}
		if len(p.GeoIPv4CIDRs) > 200000 || len(p.GeoIPv6CIDRs) > 200000 {
			return errors.New("GeoIP 地址集合超过 agent 预算，请改用仅允许中转模式或减少地区")
		}
		bytes := 0
		for _, value := range append(append([]string{}, p.GeoIPv4CIDRs...), p.GeoIPv6CIDRs...) {
			bytes += len(value)
		}
		if bytes > 700000 {
			return errors.New("GeoIP 地址集合超过 desired 配置预算，请改用仅允许中转模式或减少地区")
		}
	default:
		return errors.New("入口来源模式无效")
	}
	for _, raw := range append(append([]string{}, p.SourceCIDRs...), append(p.GeoIPv4CIDRs, p.GeoIPv6CIDRs...)...) {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix != prefix.Masked() || prefix.Addr().IsUnspecified() || prefix.Addr().IsMulticast() {
			return errors.New("入口来源地址集合包含无效或非规范网段")
		}
	}
	return nil
}

// NormalizeCountries returns a stable copy for fingerprints and desired-state
// hashes. It does not add or remove any policy entries.
func (p *IngressPolicy) NormalizeCountries() {
	if p == nil {
		return
	}
	for i := range p.Countries {
		p.Countries[i] = strings.ToUpper(strings.TrimSpace(p.Countries[i]))
	}
	sort.Strings(p.Countries)
}

func (d Direct) Validate() error {
	if d.InterfaceID != "" && !ValidIdentity(d.InterfaceID) {
		return errors.New("出口接口身份无效")
	}
	if d.Family != "dual" && d.Family != "ipv4" && d.Family != "ipv6" {
		return errors.New("出口地址族无效")
	}
	for family, source := range map[int]*Address{4: d.SourceIPv4, 6: d.SourceIPv6} {
		if source == nil {
			continue
		}
		if !ValidIdentity(source.InterfaceID) || (d.InterfaceID != "" && source.InterfaceID != d.InterfaceID) {
			return errors.New("源地址必须属于指定接口；仅绑定源地址时也必须保存所属接口身份")
		}
		a, err := HostAddress(source.Address)
		if err != nil {
			return err
		}
		if a.Is4() != (family == 4) || (d.Family == "ipv4" && family == 6) || (d.Family == "ipv6" && family == 4) {
			return errors.New("源地址与出口地址族冲突")
		}
	}
	if d.DNS.Transport != "udp" && d.DNS.Transport != "tcp" {
		return errors.New("直连出口须显式选择 UDP 或 TCP DNS")
	}
	if d.DNS.Port < 1 || d.DNS.Port > 65535 {
		return errors.New("DNS 端口无效")
	}
	a, err := HostAddress(d.DNS.Address)
	if err != nil {
		return err
	}
	if (d.Family == "ipv4" && !a.Is4()) || (d.Family == "ipv6" && a.Is4()) {
		return errors.New("DNS 传输地址与出口地址族冲突")
	}
	return nil
}

// Resolved values are agent-local observations, never accepted from a remote
// desired-state payload. Interface indices/names are compiled only after lookup
// by stable ID. The guard must revalidate these observations before activation.
type Interface struct {
	ID    string
	Name  string
	Index int
}

type ResolvedDirect struct {
	Config    Direct
	Interface *Interface
	Owners    []Interface
}

type Resolved struct {
	CollectorID, BootID string
	Sequence            int64
	ListenAddress       string
	ListenInterface     *Interface
	Direct              *ResolvedDirect
	Handshake           *ResolvedHandshake `json:",omitempty"`
	SOCKS5              *ResolvedSOCKS5
	ForwardTarget       *ResolvedForwardTarget `json:",omitempty"`
}

// ResolvedHandshake is local-only. The core dials this public literal through
// the proxy, while keeping the original TLS hostname. No second core DNS lookup
// or private transport grant is allowed for Reality camouflage.
type ResolvedHandshake struct {
	RefreshAt time.Time `json:",omitempty"`
	Host      string
	Address   string
	Port      int
}

func (h ResolvedHandshake) Validate() error {
	return (ResolvedSOCKS5{Address: h.Address, Port: h.Port}).Validate()
}

func (h ResolvedHandshake) WithoutDNSLifetime() ResolvedHandshake {
	h.RefreshAt = time.Time{}
	return h
}
func (h ResolvedHandshake) DNSRefreshDue(now time.Time) bool {
	return !h.RefreshAt.IsZero() && !now.Before(h.RefreshAt)
}
