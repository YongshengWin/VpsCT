package nodecombo

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"strings"
)

var certIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// ValidCertID accepts a local policy reference, never a filesystem path.
func ValidCertID(id string) bool { return certIDPattern.MatchString(id) && id != "." && id != ".." }

// ValidateManagedCert checks the managed TLS certificate binding, not host authorization.
func ValidateManagedCert(mode, name, id string) error {
	switch mode {
	case "self_signed", "external", "acme":
	default:
		return errors.New("无效的 TLS 证书模式")
	}
	if !ValidDNSName(name) {
		ip, err := netip.ParseAddr(name)
		if mode == "acme" || err != nil || ip.Zone() != "" {
			return errors.New("TLS 证书名称须为域名或 IP，ACME 必须使用有效域名")
		}
	}
	if mode == "external" && !ValidCertID(id) || mode != "external" && id != "" {
		return errors.New("仅外部证书模式接受并要求有效的本机证书 ID")
	}
	return nil
}

// TLSTransportVersion identifies the managed TLS wire capability.
const TLSTransportVersion = 1
const TLSTransportHeader = "X-Ctlvps-TLS-Transport-Version"

type WS struct {
	Path string `json:"path"`
	Host string `json:"host,omitempty"`
}

type GRPC struct {
	ServiceName string `json:"service_name"`
}

type Trojan struct {
	Transport string `json:"transport"`
	Security  string `json:"security"`
	WS        *WS    `json:"ws,omitempty"`
	GRPC      *GRPC  `json:"grpc,omitempty"`
}

func (t *Trojan) UnmarshalJSON(data []byte) error {
	type wire Trojan
	var w wire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return err
	}
	*t = Trojan(w)
	return nil
}

// The zero value represents native Trojan. An explicit profile must name TLS.
func (t Trojan) Normalized() (Trojan, error) {
	if t == (Trojan{}) {
		t.Transport, t.Security = "tcp", "tls"
	}
	if t.Security != "tls" {
		return Trojan{}, errors.New("Trojan 仅支持普通 TLS")
	}
	var err error
	t.WS, t.GRPC, err = normalizeTLSTransport(t.Transport, t.WS, t.GRPC)
	if err != nil {
		return Trojan{}, err
	}
	return t, nil
}

func validTransportPath(path string) bool {
	if len(path) > 512 || !pathPattern.MatchString(path) || strings.Contains(path, "//") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func normalizeTLSTransport(network string, ws *WS, grpc *GRPC) (*WS, *GRPC, error) {
	if ws != nil && network != "ws" || grpc != nil && network != "grpc" {
		return nil, nil, errors.New("传输参数必须与所选传输匹配")
	}
	switch network {
	case "tcp":
		return nil, nil, nil
	case "ws":
		w := WS{}
		if ws != nil {
			w = *ws
		}
		if w.Path == "" {
			w.Path = "/"
		}
		if !validTransportPath(w.Path) {
			return nil, nil, errors.New("WS 路径必须是绝对路径，不含查询、转义字符或相对路径段")
		}
		if w.Host != "" && !ValidDNSName(w.Host) {
			return nil, nil, errors.New("WS Host 必须为域名，不带协议、端口或路径")
		}
		return &w, nil, nil
	case "grpc":
		if grpc == nil || !serviceNamePattern.MatchString(grpc.ServiceName) {
			return nil, nil, errors.New("gRPC 服务名必须为 1–128 位，以字母或数字开头，仅含字母、数字、点、下划线或连字符")
		}
		g := *grpc
		return nil, &g, nil
	default:
		return nil, nil, errors.New("普通 TLS 的传输必须为 tcp、ws 或 grpc")
	}
}
