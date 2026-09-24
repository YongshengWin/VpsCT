// Package wgconfig defines the portable, single-peer client configuration.
package wgconfig

import (
	"crypto/ecdh"
	"ctlvps/internal/networkconfig"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

type Client struct {
	IP           string   `json:"ip,omitempty"`
	IPv6         string   `json:"ipv6,omitempty"`
	PrivateKey   string   `json:"private-key"`
	PublicKey    string   `json:"public-key"`
	PresharedKey string   `json:"pre-shared-key,omitempty"`
	AllowedIPs   []string `json:"allowed-ips,omitempty"`
	Reserved     []int    `json:"reserved,omitempty"`
	Keepalive    int      `json:"persistent-keepalive,omitempty"`
	MTU          int      `json:"mtu,omitempty"`
	UDP          bool     `json:"udp"`
	RemoteDNS    bool     `json:"remote-dns-resolve,omitempty"`
	DNS          []string `json:"dns,omitempty"`
}

func Field(k string) bool {
	switch k {
	case "ip", "ipv6", "private-key", "public-key", "pre-shared-key", "allowed-ips", "reserved", "persistent-keepalive", "mtu", "udp", "remote-dns-resolve", "dns":
		return true
	}
	return false
}
func Decode(params map[string]any) (Client, error) {
	c := Client{MTU: 1408, UDP: true}
	for k := range params {
		if !Field(k) {
			return c, errors.New("WireGuard 只支持单 Peer 标准配置，不接受脚本、文件路径或额外选项")
		}
	}
	raw, err := json.Marshal(params)
	if err != nil || len(raw) > 16384 {
		return c, errors.New("WireGuard 参数过大或格式无效")
	}
	if json.Unmarshal(raw, &c) != nil {
		return c, errors.New("WireGuard 字段类型无效")
	}
	if c.MTU < 1280 || c.MTU > 9000 || c.Keepalive < 0 || c.Keepalive > 65535 || len(c.Reserved) != 0 && len(c.Reserved) != 3 {
		return c, errors.New("WireGuard MTU、保活或 reserved 无效")
	}
	for _, v := range c.Reserved {
		if v < 0 || v > 255 {
			return c, errors.New("WireGuard reserved 字节无效")
		}
	}
	for _, key := range []string{c.PrivateKey, c.PublicKey} {
		if _, err = networkconfig.WireGuardKey(key); err != nil {
			return c, err
		}
	}
	privateBytes, _ := networkconfig.WireGuardKey(c.PrivateKey)
	publicBytes, _ := networkconfig.WireGuardKey(c.PublicKey)
	private, _ := ecdh.X25519().NewPrivateKey(privateBytes)
	public, _ := ecdh.X25519().NewPublicKey(publicBytes)
	if _, err = private.ECDH(public); err != nil {
		return c, errors.New("WireGuard Peer 公钥无效")
	}
	if c.PresharedKey != "" {
		if _, err = networkconfig.WireGuardKey(c.PresharedKey); err != nil {
			return c, err
		}
	}
	if c.IP == "" && c.IPv6 == "" {
		return c, errors.New("WireGuard 至少需要一个隧道地址")
	}
	families := map[bool]bool{}
	for _, item := range []struct {
		raw string
		v4  bool
	}{{c.IP, true}, {c.IPv6, false}} {
		if item.raw == "" {
			continue
		}
		ip, e := netip.ParseAddr(item.raw)
		if e != nil || ip.Is4() != item.v4 || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.Zone() != "" {
			return c, errors.New("WireGuard 隧道地址须为对应地址族的 IP，不带前缀")
		}
		families[item.v4] = true
	}
	if len(c.AllowedIPs) == 0 {
		if c.IP != "" {
			c.AllowedIPs = append(c.AllowedIPs, "0.0.0.0/0")
		}
		if c.IPv6 != "" {
			c.AllowedIPs = append(c.AllowedIPs, "::/0")
		}
	}
	if len(c.AllowedIPs) > 64 {
		return c, errors.New("WireGuard 目标网段过多")
	}
	seen := map[string]bool{}
	for _, raw := range c.AllowedIPs {
		p, e := netip.ParsePrefix(raw)
		if e != nil || p != p.Masked() || p.String() != raw || p.Addr().Is4In6() || !families[p.Addr().Is4()] || seen[raw] {
			return c, errors.New("WireGuard 目标网段无效或不匹配隧道地址族")
		}
		seen[raw] = true
	}
	if len(c.DNS) > 4 || c.RemoteDNS && len(c.DNS) == 0 {
		return c, errors.New("WireGuard 远端解析须配置 1–4 个 DNS 地址")
	}
	for _, raw := range c.DNS {
		ip, e := netip.ParseAddr(raw)
		if e != nil || !ip.IsGlobalUnicast() || ip.Zone() != "" || ip.Is4In6() {
			return c, errors.New("WireGuard DNS 须为 IP 地址")
		}
		allowed := false
		for _, cidr := range c.AllowedIPs {
			p, _ := netip.ParsePrefix(cidr)
			allowed = allowed || p.Contains(ip)
		}
		if !allowed {
			return c, errors.New("WireGuard DNS 不在允许网段内")
		}
	}
	return c, nil
}
func (c Client) Params() map[string]any {
	raw, _ := json.Marshal(c)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func (c Client) Addresses() []string {
	var out []string
	if c.IP != "" {
		out = append(out, c.IP+"/32")
	}
	if c.IPv6 != "" {
		out = append(out, c.IPv6+"/128")
	}
	return out
}
func (c Client) Endpoint(server string, port int) map[string]any {
	peer := map[string]any{"address": server, "port": port, "public_key": c.PublicKey, "allowed_ips": c.AllowedIPs, "persistent_keepalive_interval": c.Keepalive}
	if c.PresharedKey != "" {
		peer["pre_shared_key"] = c.PresharedKey
	}
	if len(c.Reserved) > 0 {
		peer["reserved"] = c.Reserved
	}
	return map[string]any{"type": "wireguard", "system": false, "address": c.Addresses(), "private_key": c.PrivateKey, "mtu": c.MTU, "workers": 1, "peers": []any{peer}}
}

// StandardConfig exports data only. It cannot emit PostUp/PreDown commands.
func (c Client) StandardConfig(server string, port int) (string, error) {
	if !c.UDP {
		return "", errors.New("标准 WireGuard 配置不能等价表达 TCP-only 选项，请使用 mihomo")
	}
	if strings.ContainsAny(server, "\r\n\x00 []=#;") || server == "" || port < 1 || port > 65535 {
		return "", errors.New("WireGuard 端点无效")
	}
	for _, v := range c.Reserved {
		if v != 0 {
			return "", errors.New("标准 WireGuard 配置不支持非零 reserved")
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\nMTU = %d\n", c.PrivateKey, strings.Join(c.Addresses(), ", "), c.MTU)
	if len(c.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(c.DNS, ", "))
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = %d\n", c.PublicKey, net.JoinHostPort(server, strconv.Itoa(port)), strings.Join(c.AllowedIPs, ", "), c.Keepalive)
	if c.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", c.PresharedKey)
	}
	return b.String(), nil
}
