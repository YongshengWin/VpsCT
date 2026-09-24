package networkconfig

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
)

// WireGuard represents one independently owned consumer identity. Reusing the
// same private key for different consumers would make peer endpoint roaming
// mix their sessions, so the store permanently associates each key with its node.
type WireGuard struct {
	Server                string   `json:"server"`
	ServerPort            int      `json:"server_port"`
	PublicKey             string   `json:"public_key"`
	Addresses             []string `json:"addresses"`
	AllowedIPs            []string `json:"allowed_ips"`
	MTU                   int      `json:"mtu"`
	PersistentKeepalive   int      `json:"persistent_keepalive"`
	Reserved              []int    `json:"reserved,omitempty"`
	Family                string   `json:"family"`
	DNS                   Resolver `json:"dns"`
	Outer                 Direct   `json:"outer"`
	ConnectTimeoutSeconds int      `json:"connect_timeout_seconds"`
}

func WireGuardKey(raw string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || len(b) != 32 || base64.StdEncoding.EncodeToString(b) != raw || bytes.Equal(b, make([]byte, 32)) {
		return nil, errors.New("WireGuard 密钥必须为 32 字节的规范 Base64 值")
	}
	return b, nil
}

func (w WireGuard) Transport() SOCKS5 {
	return SOCKS5{Purpose: "wireguard", Server: w.Server, ServerPort: w.ServerPort, Authentication: "none", UDP: true, Family: w.Family, DNS: w.DNS, Outer: w.Outer, ConnectTimeoutSeconds: w.ConnectTimeoutSeconds}
}

func (w WireGuard) Validate() error {
	if err := w.Transport().Validate(); err != nil {
		return err
	}
	if _, err := WireGuardKey(w.PublicKey); err != nil {
		return err
	}
	if w.MTU < 1280 || w.MTU > 9000 || w.PersistentKeepalive < 0 || w.PersistentKeepalive > 65535 || len(w.Reserved) != 0 && len(w.Reserved) != 3 {
		return errors.New("WireGuard MTU、保活间隔或 reserved 字段无效")
	}
	for _, b := range w.Reserved {
		if b < 0 || b > 255 {
			return errors.New("WireGuard reserved 字节超出范围")
		}
	}
	if len(w.Addresses) < 1 || len(w.Addresses) > 2 || len(w.AllowedIPs) < 1 || len(w.AllowedIPs) > 64 {
		return errors.New("WireGuard 须指定 1–2 个本地隧道地址及 1–64 个允许的目标网段")
	}
	families := map[bool]bool{}
	for _, raw := range w.Addresses {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p.Addr().Is4In6() || !p.Addr().IsGlobalUnicast() || p.Addr().Zone() != "" || families[p.Addr().Is4()] || w.Family == "ipv4" && !p.Addr().Is4() || w.Family == "ipv6" && p.Addr().Is4() {
			return errors.New("WireGuard 本地地址须为有效且地址族唯一的 CIDR")
		}
		families[p.Addr().Is4()] = true
	}
	if w.Family == "dual" && len(families) != 2 {
		return errors.New("双栈 WireGuard 须同时提供 IPv4 和 IPv6 隧道地址")
	}
	seen := map[string]bool{}
	dns, _ := netip.ParseAddr(w.DNS.Address)
	dnsAllowed := false
	for _, raw := range w.AllowedIPs {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p != p.Masked() || p.String() != raw || p.Addr().Is4In6() || !families[p.Addr().Is4()] || seen[raw] {
			return errors.New("WireGuard 目标网段须规范且与隧道地址族一致")
		}
		seen[raw] = true
		dnsAllowed = dnsAllowed || p.Contains(dns)
	}
	if !dnsAllowed {
		return errors.New("WireGuard 业务 DNS 必须位于允许的目标网段内")
	}
	return nil
}

func DecodeWireGuard(raw []byte) (WireGuard, error) {
	var w WireGuard
	if len(raw) == 0 || len(raw) > 16384 {
		return w, errors.New("WireGuard 配置大小无效")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&w) != nil || d.Decode(new(any)) != io.EOF {
		return w, errors.New("WireGuard 配置字段或类型无效")
	}
	return w, w.Validate()
}

func (c SOCKS5Credentials) WireGuardPublicKey() (string, error) {
	raw, err := WireGuardKey(c.WireGuardPrivateKey)
	if err != nil {
		return "", err
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", errors.New("WireGuard 私钥无效")
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func (c SOCKS5Credentials) ValidateWireGuard(w WireGuard) error {
	if c.Username != "" || c.Password != "" || c.PrivateKey != "" || c.PrivateKeyPassphrase != "" {
		return errors.New("WireGuard 不能附带其他协议凭据")
	}
	if _, err := c.WireGuardPublicKey(); err != nil {
		return err
	}
	if c.WireGuardPresharedKey != "" {
		if _, err := WireGuardKey(c.WireGuardPresharedKey); err != nil {
			return err
		}
	}
	if err := w.Validate(); err != nil {
		return err
	}
	private, _ := WireGuardKey(c.WireGuardPrivateKey)
	key, _ := ecdh.X25519().NewPrivateKey(private)
	public, _ := WireGuardKey(w.PublicKey)
	peer, err := ecdh.X25519().NewPublicKey(public)
	if err != nil {
		return errors.New("WireGuard Peer 公钥无效")
	}
	if _, err = key.ECDH(peer); err != nil {
		return errors.New("WireGuard Peer 公钥不可用")
	}
	return nil
}
