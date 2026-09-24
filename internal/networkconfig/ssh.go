package networkconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"ctlvps/internal/sshconfig"
)

// SSH contains public transport policy only. Credentials remain encrypted in
// immutable revisions, separate from client nodes and management SSH accounts.
type SSH struct {
	UDP                   bool     `json:"udp"`
	Server                string   `json:"server"`
	ServerPort            int      `json:"server_port"`
	Authentication        string   `json:"authentication"`
	HostKeys              []string `json:"host_keys"`
	Family                string   `json:"family"`
	DNS                   Resolver `json:"dns"`
	Outer                 Direct   `json:"outer"`
	ConnectTimeoutSeconds int      `json:"connect_timeout_seconds"`
}

func (s SSH) Transport() SOCKS5 {
	return SOCKS5{Server: s.Server, ServerPort: s.ServerPort, Authentication: "none", Family: s.Family, DNS: s.DNS, Outer: s.Outer, ConnectTimeoutSeconds: s.ConnectTimeoutSeconds, Purpose: "ssh"}
}

func (s SSH) Validate() error {
	if s.UDP {
		return errors.New("SSH 出口只支持 TCP")
	}
	if s.Authentication != "password" && s.Authentication != "private_key" {
		return errors.New("SSH 出口须选择密码或私钥认证")
	}
	if err := s.Transport().Validate(); err != nil {
		return err
	}
	// Reuse host-key parsing without inspecting any filesystem or private key.
	return (sshconfig.Client{Username: "validation", Password: "validation", HostKeys: s.HostKeys}).Validate()
}

func DecodeSSH(raw []byte) (SSH, error) {
	var s SSH
	if len(raw) == 0 || len(raw) > 65536 {
		return s, errors.New("SSH 配置大小无效")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return s, errors.New("SSH 配置字段或类型无效")
	}
	if err := s.Validate(); err != nil {
		return s, err
	}
	s.Server = strings.ToLower(strings.TrimSuffix(s.Server, "."))
	return s, nil
}

func (c SOCKS5Credentials) SSHClient(cfg SSH) sshconfig.Client {
	return sshconfig.Client{Username: c.Username, Password: c.Password, PrivateKey: c.PrivateKey, PrivateKeyPassphrase: c.PrivateKeyPassphrase, HostKeys: cfg.HostKeys}
}

func (c SOCKS5Credentials) ValidateSSH(cfg SSH) error {
	if c.WireGuardPrivateKey != "" || c.WireGuardPresharedKey != "" {
		return errors.New("SSH 不能附带 WireGuard 凭据")
	}
	if cfg.Authentication == "password" && (c.PrivateKey != "" || c.Password == "") || cfg.Authentication == "private_key" && (c.Password != "" || c.PrivateKey == "") {
		return errors.New("SSH 凭据与认证方式不一致")
	}
	return c.SSHClient(cfg).Validate()
}
