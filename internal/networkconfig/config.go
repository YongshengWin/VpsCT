// Package networkconfig defines server-side network policies. These values are
// separate from client protocol parameters and contain no exported credentials.
package networkconfig

import (
	"errors"
	"net/netip"
	"regexp"
)

// Shared by the protocol and the isolated HTTP helper without introducing a
// dependency from the low-privilege transport to maintenance/control types.
const BindingVersion = 1
const BindingHeader = "X-Ctlvps-Network-Binding-Version"

// Transit has its own contract: understanding direct bindings does not imply
// understanding upstream transports. Keep direct-only payloads unchanged.
const EgressVersion = 1
const EgressHeader = "X-Ctlvps-Network-Egress-Version"

// Node is persisted with a node or an automatic share-deployment target. A nil
// policy preserves legacy behavior; an explicit policy always fails closed.
type Node struct {
	ListenMode        string `json:"listen_mode"` // all | address
	ListenAddress     string `json:"listen_address,omitempty"`
	ListenInterfaceID string `json:"listen_interface_id,omitempty"`
	AdvertiseMode     string `json:"advertise_mode"` // inherit | override
	EgressProfileID   int64  `json:"egress_profile_id,omitempty"`
	EgressRevision    int64  `json:"egress_revision,omitempty"`
	OnUnavailable     string `json:"on_unavailable"` // block
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
	if n.AdvertiseMode != "inherit" && n.AdvertiseMode != "override" {
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
	default:
		return errors.New("监听模式无效")
	}
	if n.EgressProfileID < 0 || n.EgressRevision < 0 || n.EgressProfileID > 1<<53-1 || n.EgressRevision > 1<<53-1 || (n.EgressProfileID == 0) != (n.EgressRevision == 0) {
		return errors.New("出口配置和不可变版本必须同时指定")
	}
	return nil
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
	SOCKS5              *ResolvedSOCKS5
	ForwardTarget       *ResolvedForwardTarget `json:",omitempty"`
}
