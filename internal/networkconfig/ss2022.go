package networkconfig

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
)

// SS2022 is an encrypted fixed upstream. The key lives only in the existing
// encrypted credential store and the authenticated agent payload.
type SS2022 struct {
	Server                string   `json:"server"`
	ServerPort            int      `json:"server_port"`
	Method                string   `json:"method"`
	Family                string   `json:"family"`
	DNS                   Resolver `json:"dns"`
	Outer                 Direct   `json:"outer"`
	ConnectTimeoutSeconds int      `json:"connect_timeout_seconds"`
}

func (s SS2022) Transport() SOCKS5 {
	return SOCKS5{Purpose: "ss2022", Server: s.Server, ServerPort: s.ServerPort,
		Authentication: "none", UDP: true, Family: s.Family, DNS: s.DNS,
		Outer: s.Outer, ConnectTimeoutSeconds: s.ConnectTimeoutSeconds}
}

func (s SS2022) Validate() error {
	if s.Method != "2022-blake3-aes-128-gcm" && s.Method != "2022-blake3-aes-256-gcm" {
		return errors.New("SS-2022 目前仅支持 AES-128-GCM 或 AES-256-GCM")
	}
	return s.Transport().Validate()
}

func (c SOCKS5Credentials) ValidateSS2022(method string) error {
	if c.Username != "" || c.PrivateKey != "" || c.PrivateKeyPassphrase != "" || c.WireGuardPrivateKey != "" || c.WireGuardPresharedKey != "" {
		return errors.New("SS-2022 密钥字段无效")
	}
	n := 16
	if method == "2022-blake3-aes-256-gcm" {
		n = 32
	} else if method != "2022-blake3-aes-128-gcm" {
		return errors.New("SS-2022 加密方法无效")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(c.Password)
	if err != nil || len(b) != n || base64.StdEncoding.EncodeToString(b) != c.Password {
		return errors.New("SS-2022 密钥须为对应长度的规范 Base64")
	}
	return nil
}

func DecodeSS2022(raw []byte) (SS2022, error) {
	var s SS2022
	if len(raw) == 0 || len(raw) > 16384 {
		return s, errors.New("SS-2022 配置大小无效")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil || d.Decode(new(any)) != io.EOF {
		return SS2022{}, errors.New("SS-2022 配置字段无效")
	}
	if err := s.Validate(); err != nil {
		return SS2022{}, err
	}
	if ip, err := netip.ParseAddr(s.Server); err == nil {
		s.Server = ip.String()
	} else {
		s.Server = strings.ToLower(strings.TrimSuffix(s.Server, "."))
	}
	return s, nil
}
