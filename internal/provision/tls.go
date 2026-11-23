package provision

import (
	"encoding/json"
	"errors"
	"reflect"

	"ctlvps/internal/domain"
	"ctlvps/internal/nodecombo"
)

// setTLSTransport writes only transport choices, never certificate or credential material.
func setTLSTransport(client, srv map[string]any, network string, ws *nodecombo.WS, grpc *nodecombo.GRPC) {
	srv["security"], srv["network"], client["network"] = "tls", network, network
	if ws != nil {
		o := map[string]any{"path": ws.Path}
		srv["ws_path"] = ws.Path
		if ws.Host != "" {
			srv["ws_host"] = ws.Host
			o["headers"] = map[string]any{"Host": ws.Host}
		}
		client["ws-opts"] = o
	}
	if grpc != nil {
		srv["grpc_service_name"] = grpc.ServiceName
		client["grpc-opts"] = map[string]any{"grpc-service-name": grpc.ServiceName}
	}
}

func hasAny(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func sameFields(a, b map[string]any, keys ...string) bool {
	for _, k := range keys {
		x, ok := a[k]
		y, want := b[k]
		if ok != want || !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// recoverTLSProfile is shared by the two protocols. The bool distinguishes TLS
// profiles from legacy REALITY, where missing security must not imply ordinary TLS.
func recoverTLSProfile(n domain.Node) (*nodecombo.Trojan, bool, error) {
	invalid := errors.New("无法无损恢复节点的 TLS 组合")
	var s, c map[string]any
	if json.Unmarshal(n.ServerParams, &s) != nil || s == nil || json.Unmarshal(n.Params, &c) != nil || c == nil {
		return nil, false, invalid
	}
	if security, explicit := s["security"]; !explicit || n.Protocol == domain.ProtocolVLESS && security == "reality" {
		if hasAny(s, "ws_path", "ws_host", "grpc_service_name") || hasAny(c, "ws-opts", "grpc-opts") {
			return nil, false, invalid
		}
		if n.Protocol == domain.ProtocolVLESS {
			return nil, false, nil
		}
		if n.Core != domain.CoreSingBox || hasAny(s, "network", "flow", "xhttp_path", "xhttp_mode", "xhttp_host", "reality_private_key", "reality_public_key", "reality_short_id", "handshake_server", "handshake_port") || hasAny(c, "network", "flow", "reality-opts", "xhttp-opts") {
			return nil, false, invalid
		}
		return &nodecombo.Trojan{Transport: "tcp", Security: "tls"}, true, nil
	}
	if n.Core != domain.CoreSingBox || s["security"] != "tls" || hasAny(s, "flow", "xhttp_path", "xhttp_host", "xhttp_mode", "reality_private_key", "reality_public_key", "reality_short_id", "handshake_server", "handshake_port") || hasAny(c, "flow", "xhttp-opts", "reality-opts") {
		return nil, false, invalid
	}
	str := func(key string) string { value, _ := s[key].(string); return value }
	t := nodecombo.Trojan{Transport: str("network"), Security: "tls"}
	if n.Network != nil && (n.Protocol == domain.ProtocolVLESS || t.Transport != "tcp") {
		return nil, false, errors.New("普通 TLS 新组合暂不支持自定义网络策略")
	}
	switch t.Transport {
	case "ws":
		t.WS = &nodecombo.WS{Path: str("ws_path"), Host: str("ws_host")}
	case "grpc":
		t.GRPC = &nodecombo.GRPC{ServiceName: str("grpc_service_name")}
	case "tcp":
	default:
		return nil, false, invalid
	}
	t, err := t.Normalized()
	if err != nil {
		return nil, false, err
	}
	wantC, wantS := map[string]any{}, map[string]any{}
	setTLSTransport(wantC, wantS, t.Transport, t.WS, t.GRPC)
	if !sameFields(c, wantC, "network", "ws-opts", "grpc-opts") || !sameFields(s, wantS, "network", "ws_path", "ws_host", "grpc_service_name") {
		return nil, false, invalid
	}
	if err := nodecombo.ValidateManagedCert(str("cert_mode"), str("tls_domain"), str("cert_id")); err != nil {
		return nil, false, err
	}
	if _, ok := s["cert_id"]; ok && s["cert_mode"] != "external" {
		return nil, false, invalid
	}
	nameKey := "sni"
	if n.Protocol == domain.ProtocolVLESS {
		nameKey = "servername"
		if c["tls"] != true {
			return nil, false, invalid
		}
	}
	if c[nameKey] != str("tls_domain") {
		return nil, false, invalid
	}
	if skip, exists := c["skip-cert-verify"]; (exists || s["cert_mode"] == "self_signed") && skip != (s["cert_mode"] == "self_signed") {
		return nil, false, invalid
	}
	return &t, true, nil
}

// TrojanFromNode returns the canonical TCP/TLS profile for unmarked native nodes.
func TrojanFromNode(n domain.Node) (*nodecombo.Trojan, error) {
	if n.Protocol != domain.ProtocolTrojan {
		return nil, errors.New("节点不是 Trojan")
	}
	t, _, err := recoverTLSProfile(n)
	return t, err
}
