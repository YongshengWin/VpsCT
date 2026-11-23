package agentproto

import (
	"errors"

	"ctlvps/internal/nodecombo"
)

// TLSTransportVersion is a wire contract, not a sing-box binary version.
const TLSTransportVersion = nodecombo.TLSTransportVersion
const TLSTransportHeader = nodecombo.TLSTransportHeader

// NeedsTLSTransport also recognizes malformed combinations so they cannot
// fall through to legacy TCP / REALITY / Vision rendering.
func NeedsTLSTransport(protocol string, p map[string]any) bool {
	if protocol != "vless" && protocol != "trojan" {
		return false
	}
	if _, cert := p["cert_mode"]; protocol == "vless" && cert {
		return true
	}
	for _, key := range []string{"ws_path", "ws_host", "grpc_service_name"} {
		if _, ok := p[key]; ok {
			return true
		}
	}
	if v, ok := p["network"]; ok && v != "tcp" && !(protocol == "vless" && v == "xhttp") {
		return true
	}
	if v, ok := p["security"]; ok {
		if protocol == "vless" {
			return v != "reality"
		}
		return v != "tls"
	}
	return false
}

// ValidateTLSTransportParams is shared by transactional admission and the
// execution boundary. Only modeled parameters reach the official inbound.
func ValidateTLSTransportParams(protocol, core string, p map[string]any) error {
	if core != "singbox" || (protocol != "vless" && protocol != "trojan") {
		return errors.New("TLS 传输需要 sing-box VLESS 或 Trojan")
	}
	get := func(k string) string { v, _ := p[k].(string); return v }
	net := get("network")
	if net != "tcp" && net != "ws" && net != "grpc" {
		return errors.New("普通 TLS 节点需要明确的 TCP、WS 或 gRPC 传输")
	}
	var ws *nodecombo.WS
	var grpc *nodecombo.GRPC
	for k, v := range p {
		if _, ok := v.(string); !ok {
			return errors.New("TLS 传输参数类型无效")
		}
		switch k {
		case "network", "security", "cert_id", "cert_mode", "tls_domain":
		case "uuid", "flow":
			if protocol != "vless" {
				return errors.New("Trojan 不支持 VLESS 参数")
			}
		case "password":
			if protocol != "trojan" {
				return errors.New("VLESS 不支持密码参数")
			}
		case "ws_path", "ws_host":
			if net != "ws" {
				return errors.New("非 WS 节点不能携带 WS 参数")
			}
		case "grpc_service_name":
			if net != "grpc" {
				return errors.New("非 gRPC 节点不能携带 gRPC 参数")
			}
		default:
			return errors.New("普通 TLS 节点包含不支持的参数")
		}
	}
	if err := nodecombo.ValidateManagedCert(get("cert_mode"), get("tls_domain"), get("cert_id")); err != nil {
		return err
	}
	if _, exists := p["cert_id"]; exists && get("cert_mode") != "external" {
		return errors.New("非外部证书节点不能携带证书 ID")
	}
	if net == "ws" {
		if get("ws_path") == "" {
			return errors.New("需要明确的 WS 路径")
		}
		ws = &nodecombo.WS{Path: get("ws_path"), Host: get("ws_host")}
	}
	if net == "grpc" {
		grpc = &nodecombo.GRPC{ServiceName: get("grpc_service_name")}
	}
	if err := ValidateParams(p, 0); err != nil {
		return err
	}
	if protocol == "vless" {
		if get("security") != "tls" || get("flow") != "" || !xrayUUID.MatchString(get("uuid")) {
			return errors.New("VLESS 普通 TLS 需要有效的 UUID，不能启用 REALITY 或 Vision")
		}
		_, err := (nodecombo.VLESS{Transport: net, Security: "tls", WS: ws, GRPC: grpc}).Normalized()
		return err
	}
	if sec, ok := p["security"]; ok && sec != "tls" || get("password") == "" {
		return errors.New("Trojan 需要普通 TLS 和密码")
	}
	_, err := (nodecombo.Trojan{Transport: net, Security: "tls", WS: ws, GRPC: grpc}).Normalized()
	return err
}

func ValidateTLSTransportNode(ds *DesiredState, n NodeSpec) error {
	if ds == nil || ds.TLSTransportVersion != TLSTransportVersion || n.Cert == nil {
		return errors.New("普通 TLS 传输缺少 agent 能力或证书")
	}
	if n.Network != nil || n.RuntimeNetwork != nil {
		return errors.New("普通 TLS 新传输暂不支持自定义网络绑定或出口")
	}
	id, _ := n.Params["cert_id"].(string)
	if n.Params["cert_mode"] != n.Cert.Mode || n.Params["tls_domain"] != n.Cert.Domain || id != n.Cert.ID || n.Cert.Email != "" {
		return errors.New("TLS 证书配置与节点参数不一致")
	}
	return ValidateTLSTransportParams(n.Protocol, n.Core, n.Params)
}
