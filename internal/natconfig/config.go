// Package natconfig reads the optional root-owned port mapping for a NAT VPS.
// Public ports remain the controller's node identity; local ports describe
// where the provider delivers those packets to this host.
package natconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"

	"ctlvps/internal/secureupdate"
)

const Path = "/etc/ctlvps/agent-nat.json"

// MaxPortPairs bounds expanded public/local pairs before their TCP/UDP split.
const MaxPortPairs = 4096

type Config struct {
	Schema       int       `json:"schema"`
	Interface    string    `json:"interface,omitempty"`
	LocalAddress string    `json:"local_address,omitempty"`
	Mappings     []Mapping `json:"mappings"`
}

// Mapping pairs consecutive provider public and local ports. Count defaults to
// one and Network defaults to both when omitted.
type Mapping struct {
	PublicPort int    `json:"public_port"`
	LocalPort  int    `json:"local_port"`
	Count      int    `json:"count,omitempty"`
	Network    string `json:"network,omitempty"`
}

// PortMapping is one port pair for exactly one transport (tcp or udp).
type PortMapping struct {
	PublicPort int    `json:"public_port"`
	LocalPort  int    `json:"local_port"`
	Network    string `json:"network"`
}

// Load treats a missing local configuration as an opt-out. Untrusted files and
// invalid contents are errors, never an implicit opt-out.
func Load() (Config, error) {
	c, _, err := LoadLocal()
	return c, err
}

// LoadLocal distinguishes an absent local authority from an explicit local
// opt-out. Any unsafe or unreadable file remains authoritative and fails closed.
func LoadLocal() (Config, bool, error) {
	return loadLocal(secureupdate.ReadProtectedConfig)
}

func loadLocal(read func(string) ([]byte, error)) (Config, bool, error) {
	b, err := read(Path)
	if os.IsNotExist(err) {
		return Config{Schema: 1}, false, nil
	}
	if err != nil {
		return Config{}, true, err
	}
	c, err := Decode(b)
	return c, true, err
}

// Decode rejects unknown fields and trailing JSON, then normalizes defaults.
func Decode(b []byte) (Config, error) {
	var c Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("NAT 配置 JSON 无效: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("NAT 配置不允许尾随 JSON 或其他内容")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c.Normalized(), nil
}

// Normalized returns an independent snapshot with the same meaning as Expand.
// It does not replace validation at an execution or API boundary.
func (c Config) Normalized() Config {
	c.Mappings = append([]Mapping{}, c.Mappings...)
	for i := range c.Mappings {
		if c.Mappings[i].Count == 0 {
			c.Mappings[i].Count = 1
		}
		if c.Mappings[i].Network == "" {
			c.Mappings[i].Network = "both"
		}
	}
	return c
}

func (c Config) Validate() error {
	_, err := c.Expand()
	return err
}

// Expand validates and returns a deterministic list. Public and local ports
// must each be unique within a transport; TCP and UDP may be mapped separately.
// A different-port mapping may never intercept a privileged local port.
func (c Config) Expand() ([]PortMapping, error) {
	if c.Schema != 1 {
		return nil, errors.New("NAT 配置 schema 必须为 1")
	}
	if c.Interface != "" && !validInterface(c.Interface) {
		return nil, errors.New("NAT interface 必须为 1–15 字节的网卡名称，仅支持字母、数字、下划线、点和连字符")
	}
	if c.LocalAddress != "" {
		a, err := netip.ParseAddr(c.LocalAddress)
		if err != nil || !a.Is4() || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			return nil, errors.New("NAT local_address 必须为非回环的 IPv4 单播地址")
		}
	}
	if len(c.Mappings) > MaxPortPairs {
		return nil, fmt.Errorf("NAT 映射最多允许 %d 个端口对", MaxPortPairs)
	}
	type endpoint struct {
		port    int
		network string
	}
	public, local := map[endpoint]bool{}, map[endpoint]bool{}
	out := make([]PortMapping, 0)
	total := 0
	for i, m := range c.Mappings {
		count := m.Count
		if count == 0 {
			count = 1
		}
		if m.PublicPort < 1 || m.PublicPort > 65535 || m.LocalPort < 1 || m.LocalPort > 65535 || count < 1 || count > 65536-m.PublicPort || count > 65536-m.LocalPort {
			return nil, fmt.Errorf("NAT 第 %d 条映射端口范围无效", i+1)
		}
		if count > MaxPortPairs-total {
			return nil, fmt.Errorf("NAT 映射最多允许 %d 个端口对", MaxPortPairs)
		}
		total += count
		if m.PublicPort != m.LocalPort && m.LocalPort < 1024 {
			return nil, fmt.Errorf("NAT 第 %d 条映射不能转换本机 1–1023 端口", i+1)
		}
		network := m.Network
		if network == "" {
			network = "both"
		}
		var transports []string
		switch network {
		case "both":
			transports = []string{"tcp", "udp"}
		case "tcp", "udp":
			transports = []string{network}
		default:
			return nil, fmt.Errorf("NAT 第 %d 条映射 network 只支持 tcp、udp 或 both", i+1)
		}
		for offset := 0; offset < count; offset++ {
			for _, transport := range transports {
				p := PortMapping{PublicPort: m.PublicPort + offset, LocalPort: m.LocalPort + offset, Network: transport}
				pub, loc := endpoint{p.PublicPort, transport}, endpoint{p.LocalPort, transport}
				if public[pub] {
					return nil, fmt.Errorf("NAT 公网 %s 端口 %d 存在重复映射", transport, p.PublicPort)
				}
				if local[loc] {
					return nil, fmt.Errorf("NAT 本机 %s 端口 %d 存在重复映射", transport, p.LocalPort)
				}
				public[pub], local[loc] = true, true
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PublicPort != out[j].PublicPort {
			return out[i].PublicPort < out[j].PublicPort
		}
		if out[i].LocalPort != out[j].LocalPort {
			return out[i].LocalPort < out[j].LocalPort
		}
		return out[i].Network < out[j].Network
	})
	return out, nil
}

func validInterface(name string) bool {
	if len(name) == 0 || len(name) > 15 || name == "." || name == ".." {
		return false
	}
	for i := range name {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' {
			continue
		}
		return false
	}
	return true
}
