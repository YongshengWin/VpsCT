package provision

import (
	"strings"

	"ctlvps/internal/domain"
)

// ProtocolDisplayName is the canonical capitalized protocol label used in
// default node names (AnyTLS, VLESS, Hysteria2, …).
func ProtocolDisplayName(proto string) string {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case domain.ProtocolAnyTLS:
		return "AnyTLS"
	case domain.ProtocolVLESS:
		return "VLESS"
	case domain.ProtocolHysteria2:
		return "Hysteria2"
	case domain.ProtocolTUIC:
		return "TUIC"
	case domain.ProtocolTrojan:
		return "Trojan"
	case domain.ProtocolShadowsocks:
		return "SS"
	case domain.ProtocolSnell:
		return "Snell"
	case domain.ProtocolVMess:
		return "VMess"
	case domain.ProtocolSocks5:
		return "SOCKS5"
	case domain.ProtocolHTTP:
		return "HTTP"
	default:
		if proto == "" {
			return ""
		}
		return proto
	}
}

// DefaultNodeName is 服务器名-地区码-协议, e.g. "Zouter-HK-AnyTLS".
func DefaultNodeName(server domain.Server, protocol string) string {
	name := strings.TrimSpace(server.Name)
	region := strings.ToUpper(strings.TrimSpace(server.Region))
	proto := ProtocolDisplayName(protocol)
	var parts []string
	if name != "" {
		parts = append(parts, name)
	}
	if region != "" {
		parts = append(parts, region)
	}
	if proto != "" {
		parts = append(parts, proto)
	}
	return strings.Join(parts, "-")
}
