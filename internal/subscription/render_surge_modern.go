package subscription

import (
	"strconv"
	"strings"

	"ctlvps/internal/proxynode"
)

// ProfileSurgeVLESS is opt-in. The fingerprint names require Surge iOS
// 6.1+/Mac 6.13+; never infer this profile from an unversioned user agent.
const ProfileSurgeVLESS = "surge-vless"

// Unlike the legacy proxy-only include, this is a complete profile and can
// carry WireGuard sections. The default never routes traffic directly.
const surgeFullTemplate = `[Proxy]
{{PROXIES}}

[Proxy Group]
VpsCT Nodes = select,{{all}}
{{PROXY_GROUPS}}

[Rule]
{{RULES}}
FINAL,VpsCT Nodes
`

// The supported client syntax and combinations are documented at
// https://manual.nssurge.com/policies/vless.html. Unknown transport/security
// fields are not options we can copy or ignore (e.g. downloadSettings).
func modernVLESSLine(p proxynode.Proxy, via string) string {
	for key := range p.Params {
		if !oneOf(key, "uuid", "tls", "network", "udp", "tfo", "servername", "sni", "flow", "client-fingerprint", "encryption", "packet-encoding", "ws-opts", "grpc-opts", "xhttp-opts", "reality-opts", "alpn", "skip-cert-verify", "dialer-proxy") {
			return ""
		}
	}
	if p.Str("uuid") == "" || !oneOf(p.Str("encryption"), "", "none") || !oneOf(p.Str("packet-encoding"), "", "xudp") || (p.Params["udp"] != nil && !p.Bool("udp")) {
		return ""
	}
	parts := []string{"vless", p.Server, strconv.Itoa(p.Port)}
	valid := true
	add := func(key, value string) {
		if value == "" {
			return
		}
		if strings.ContainsAny(value, "\r\n\x00\"") {
			valid = false
			return
		}
		if strings.Contains(value, ",") {
			value = surgeQuote(value)
		}
		parts = append(parts, key+"="+value)
	}
	add("username", p.Str("uuid"))
	sni := nonempty(p.Str("servername"), p.Str("sni"))
	add("sni", sni)
	ro := p.Sub("reality-opts")
	if _, has := p.Params["reality-opts"]; has {
		if ro == nil || sni == "" || mapStr(ro, "public-key") == "" || p.Bool("skip-cert-verify") {
			return ""
		}
		for k := range ro {
			if !oneOf(k, "public-key", "short-id") {
				return ""
			}
		}
		add("reality-public-key", mapStr(ro, "public-key"))
		add("reality-short-id", mapStr(ro, "short-id"))
	} else if p.Bool("tls") {
		add("tls", "true")
		if p.Bool("skip-cert-verify") {
			add("skip-cert-verify", "true")
		}
	} else if p.Bool("skip-cert-verify") || sni != "" || len(p.StrList("alpn")) > 0 {
		return ""
	}
	fp := p.Str("client-fingerprint")
	switch fp {
	case "", "auto", "apple-26", "chromium-155":
	case "chrome", "edge", "android", "360", "qq":
		fp = "chromium-155"
	case "safari", "ios":
		fp = "apple-26"
	default:
		return ""
	}
	add("client-fingerprint", fp)
	add("alpn", strings.Join(p.StrList("alpn"), ","))
	network := p.Str("network")
	for _, opt := range []string{"ws", "grpc", "xhttp"} {
		if _, has := p.Params[opt+"-opts"]; has && opt != network {
			return ""
		}
	}
	switch network {
	case "ws":
		if p.Str("flow") != "" || !addSurgeWS(p, add) {
			return ""
		}
	case "grpc":
		service, ok := p.BasicGRPCService()
		if !ok || (!p.Bool("tls") && ro == nil) || p.Str("flow") != "" {
			return ""
		}
		add("grpc", "true")
		add("grpc-service-name", service)
	case "", "tcp":
		if flow := p.Str("flow"); flow != "" {
			if !oneOf(flow, "xtls-rprx-vision", "xtls-rprx-vision-udp443") || (!p.Bool("tls") && ro == nil) {
				return ""
			}
			add("flow", flow)
			if flow == "xtls-rprx-vision" {
				add("block-quic", "on")
			} else {
				add("block-quic", "off")
			}
		}
	case "xhttp":
		if p.Str("flow") != "" {
			return ""
		}
		add("xhttp", "true")
		o := p.Sub("xhttp-opts")
		if _, has := p.Params["xhttp-opts"]; has && o == nil {
			return ""
		}
		for k, v := range o {
			if !oneOf(k, "path", "host", "mode") {
				return ""
			}
			if _, ok := v.(string); !ok {
				return ""
			}
		}
		if !oneOf(mapStr(o, "mode"), "", "auto", "stream-one", "stream-up", "packet-up") {
			return ""
		}
		for _, k := range []string{"path", "host", "mode"} {
			add("xhttp-"+k, mapStr(o, k))
		}
	default:
		return ""
	}
	if p.Bool("tfo") {
		add("tfo", "true")
	}
	if via != "" {
		parts = append(parts, "underlying-proxy="+surgeQuote(via))
	}
	if !valid {
		return ""
	}
	return surgeIdent(p.Name) + " = " + strings.Join(parts, ", ")
}
