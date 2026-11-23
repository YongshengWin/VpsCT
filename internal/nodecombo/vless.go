// Package nodecombo models managed connection options, not host access policy
// or credentials. A transport can only be selected with its own options.
package nodecombo

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"strings"
)

// XrayVersion identifies the agent capability for managed XHTTP nodes.
const XrayVersion = 1
const XrayHeader = "X-Ctlvps-Xray-Version"

const Vision = "xtls-rprx-vision"

type VLESS struct {
	Transport string `json:"transport"`
	Security  string `json:"security,omitempty"`
	Flow      string `json:"flow,omitempty"`
	XHTTP     *XHTTP `json:"xhttp,omitempty"`
	WS        *WS    `json:"ws,omitempty"`
	GRPC      *GRPC  `json:"grpc,omitempty"`
}

type XHTTP struct {
	Path string `json:"path,omitempty"`
	Host string `json:"host,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// Reject unmodelled security or transport options instead of silently dropping them.
func (v *VLESS) UnmarshalJSON(data []byte) error {
	type wire VLESS
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w wire
	if err := dec.Decode(&w); err != nil {
		return err
	}
	*v = VLESS(w)
	return nil
}

var pathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
var labelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// ValidDNSName accepts only DNS names, not IP literals, URLs, wildcard names,
// header values, or kernel-specific target syntax.
func ValidDNSName(name string) bool {
	if len(name) > 253 || !strings.Contains(name, ".") || strings.HasSuffix(strings.ToLower(name), ".localhost") {
		return false
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !labelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

// Normalized returns a detached copy. The zero value is the legacy REALITY/Vision profile.
func (v VLESS) Normalized() (VLESS, error) {
	if v.Security == "" {
		v.Security = "reality"
	}
	if v.Security == "tls" {
		if v.Flow != "" || v.XHTTP != nil {
			return VLESS{}, errors.New("普通 TLS 不能携带 flow 或 XHTTP 参数")
		}
		var err error
		v.WS, v.GRPC, err = normalizeTLSTransport(v.Transport, v.WS, v.GRPC)
		if err != nil {
			return VLESS{}, err
		}
		return v, nil
	}
	if v.Security != "reality" || v.WS != nil || v.GRPC != nil {
		return VLESS{}, errors.New("无效的 VLESS 安全或传输组合")
	}
	if v.Transport == "" {
		v.Transport = "tcp"
	}
	switch v.Transport {
	case "tcp":
		if v.XHTTP != nil {
			return VLESS{}, errors.New("TCP 不能携带 XHTTP 参数")
		}
		if v.Flow == "" {
			v.Flow = Vision
		}
		if v.Flow != Vision {
			return VLESS{}, errors.New("受管 VLESS TCP 仅支持 Vision flow")
		}
	case "xhttp":
		if v.Flow != "" {
			return VLESS{}, errors.New("XHTTP 不能启用 Vision flow")
		}
		x := XHTTP{}
		if v.XHTTP != nil {
			x = *v.XHTTP
		}
		if x.Path == "" {
			x.Path = "/"
		}
		if x.Mode == "" {
			x.Mode = "auto"
		}
		if !validTransportPath(x.Path) {
			return VLESS{}, errors.New("XHTTP 路径必须是绝对路径，不含查询、转义字符或相对路径段")
		}
		if x.Host != "" && !ValidDNSName(x.Host) {
			return VLESS{}, errors.New("XHTTP Host 必须为域名，不带协议、端口或路径")
		}
		switch x.Mode {
		case "auto", "packet-up", "stream-up", "stream-one":
		default:
			return VLESS{}, errors.New("不支持的 XHTTP 模式")
		}
		v.XHTTP = &x
	default:
		return VLESS{}, errors.New("受管 VLESS 传输只支持 TCP 或 XHTTP")
	}
	return v, nil
}
