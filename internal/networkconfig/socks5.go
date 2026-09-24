package networkconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
)

// SOCKS5 separates business DNS/family from the direct path to the upstream.
// Outer.DNS is bootstrap DNS only. Business DNS always travels inside SOCKS.
// Credentials are deliberately absent from this public immutable configuration.
type SOCKS5 struct {
	// Purpose is local resolver input, never decoded from an upstream config.
	Purpose               string   `json:"-"`
	Server                string   `json:"server"`
	ServerPort            int      `json:"server_port"`
	Authentication        string   `json:"authentication"` // none | password
	UDP                   bool     `json:"udp"`
	Family                string   `json:"family"`
	DNS                   Resolver `json:"dns"`
	Outer                 Direct   `json:"outer"`
	ConnectTimeoutSeconds int      `json:"connect_timeout_seconds"`
}

// SOCKS5Credentials may appear only in encrypted storage and agent payloads.
// Ordinary profile/revision responses and client subscriptions must not expose it.
type SOCKS5Credentials struct {
	Username              string `json:"username"`
	Password              string `json:"password"`
	PrivateKey            string `json:"private_key,omitempty"`
	PrivateKeyPassphrase  string `json:"private_key_passphrase,omitempty"`
	WireGuardPrivateKey   string `json:"wireguard_private_key,omitempty"`
	WireGuardPresharedKey string `json:"wireguard_preshared_key,omitempty"`
}

func (s SOCKS5) Validate() error {
	if s.Purpose != "" && s.Purpose != "ssh" && s.Purpose != "wireguard" && s.Purpose != "ss2022" || s.Purpose == "ssh" && s.UDP || (s.Purpose == "wireguard" || s.Purpose == "ss2022") && !s.UDP {
		return errors.New("上游传输用途无效")
	}
	if err := ValidateAdvertiseHost(s.Server); err != nil {
		return errors.New("SOCKS5 上游须为有效 IP 或域名，不能包含协议、端口或路径")
	}
	if s.ServerPort < 1 || s.ServerPort > 65535 || s.ConnectTimeoutSeconds < 1 || s.ConnectTimeoutSeconds > 60 {
		return errors.New("SOCKS5 端口或连接超时无效（超时须为 1–60 秒）")
	}
	if s.Authentication != "none" && s.Authentication != "password" {
		return errors.New("SOCKS5 认证方式无效")
	}
	if err := s.Outer.Validate(); err != nil {
		return err
	}
	// Direct validation already checks resolver address, family and transport.
	if err := (Direct{Family: s.Family, DNS: s.DNS}).Validate(); err != nil {
		return err
	}
	if !s.UDP && s.DNS.Transport != "tcp" {
		return errors.New("仅 TCP 的 SOCKS5 出口必须使用 TCP 业务 DNS")
	}
	if ip, err := netip.ParseAddr(s.Server); err == nil {
		if (s.Outer.Family == "ipv4" && !ip.Is4()) || (s.Outer.Family == "ipv6" && ip.Is4()) {
			return errors.New("SOCKS5 上游地址与外层地址族冲突")
		}
	}
	return nil
}

func (c SOCKS5Credentials) Validate(authentication string) error {
	if c.PrivateKey != "" || c.PrivateKeyPassphrase != "" || c.WireGuardPrivateKey != "" || c.WireGuardPresharedKey != "" {
		return errors.New("SOCKS5 不能附带 SSH 私钥")
	}
	if authentication == "none" {
		if c.Username != "" || c.Password != "" {
			return errors.New("无认证出口不能附带用户名或密码")
		}
		return nil
	}
	if authentication != "password" || len(c.Username) < 1 || len(c.Username) > 255 || len(c.Password) < 1 || len(c.Password) > 255 || strings.ContainsRune(c.Username, 0) || strings.ContainsRune(c.Password, 0) {
		return errors.New("SOCKS5 用户名和密码须各为 1–255 字节且不含空字符")
	}
	return nil
}

func DecodeSOCKS5(raw []byte) (SOCKS5, error) {
	var s SOCKS5
	if len(raw) == 0 || len(raw) > 16384 {
		return s, errors.New("SOCKS5 配置大小无效")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, errors.New("SOCKS5 配置字段或类型无效")
	}
	if d.Decode(new(any)) != io.EOF {
		return s, errors.New("SOCKS5 配置包含多余内容")
	}
	if err := s.Validate(); err != nil {
		return s, err
	}
	if ip, err := netip.ParseAddr(s.Server); err == nil {
		s.Server = ip.String()
	} else {
		s.Server = strings.ToLower(strings.TrimSuffix(s.Server, "."))
	}
	return s, nil
}
