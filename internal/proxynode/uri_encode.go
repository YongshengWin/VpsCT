package proxynode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ToURI renders a share link for the proxy. Returns ErrUnsupported for
// protocols without a link format.
func ToURI(p Proxy) (string, error) {
	switch p.Type {
	case "ss":
		return encodeSS(p), nil
	case "vmess":
		return encodeVMess(p), nil
	case "vless":
		return encodeVLESS(p), nil
	case "trojan":
		return encodeTrojan(p), nil
	case "hysteria2":
		return encodeHysteria2(p), nil
	case "tuic":
		return encodeTUIC(p), nil
	case "anytls":
		return encodeAnyTLS(p), nil
	case "snell":
		return encodeSnell(p), nil
	case "socks5":
		return encodeSimple("socks5", p), nil
	case "http":
		scheme := "http"
		if p.Bool("tls") {
			scheme = "https"
		}
		return encodeSimple(scheme, p), nil
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupported, p.Type)
}

func frag(name string) string {
	if name == "" {
		return ""
	}
	return "#" + url.PathEscape(name)
}

func setIf(v url.Values, key, val string) {
	if val != "" {
		v.Set(key, val)
	}
}

func encodeQuery(v url.Values) string {
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func encodeSS(p Proxy) string {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(p.Str("cipher") + ":" + p.Str("password")))
	if strings.HasPrefix(p.Str("cipher"), "2022-") {
		userinfo = url.PathEscape(p.Str("cipher") + ":" + p.Str("password"))
	}
	v := url.Values{}
	if plugin := p.Str("plugin"); plugin != "" {
		opts := p.Sub("plugin-opts")
		var parts []string
		switch plugin {
		case "obfs":
			parts = append(parts, "obfs-local", "obfs="+str(opts["mode"]))
			if h := str(opts["host"]); h != "" {
				parts = append(parts, "obfs-host="+h)
			}
		case "v2ray-plugin":
			parts = append(parts, "v2ray-plugin")
			if toBool(opts["tls"]) {
				parts = append(parts, "tls")
			}
			if h := str(opts["host"]); h != "" {
				parts = append(parts, "host="+h)
			}
			if pa := str(opts["path"]); pa != "" {
				parts = append(parts, "path="+pa)
			}
		case "shadow-tls":
			parts = append(parts, "shadow-tls")
			if h := str(opts["host"]); h != "" {
				parts = append(parts, "host="+h)
			}
			if pw := str(opts["password"]); pw != "" {
				parts = append(parts, "password="+pw)
			}
			if ver := toInt(opts["version"]); ver != 0 {
				parts = append(parts, "version="+strconv.Itoa(ver))
			}
		}
		if len(parts) > 0 {
			v.Set("plugin", strings.Join(parts, ";"))
		}
	}
	if p.Bool("udp-over-tcp") {
		v.Set("udp-over-tcp", "true")
	}
	return "ss://" + userinfo + "@" + p.HostPort() + encodeQuery(v) + frag(p.Name)
}

func encodeVMess(p Proxy) string {
	m := map[string]any{
		"v":    "2",
		"ps":   p.Name,
		"add":  p.Server,
		"port": strconv.Itoa(p.Port),
		"id":   p.Str("uuid"),
		"aid":  strconv.Itoa(p.Int("alterId")),
		"scy":  orDefault(p.Str("cipher"), "auto"),
		"net":  "tcp",
		"type": "none",
		"host": "",
		"path": "",
		"tls":  "",
	}
	if p.Bool("tls") {
		m["tls"] = "tls"
		m["sni"] = p.Str("servername")
		if alpn := p.StrList("alpn"); len(alpn) > 0 {
			m["alpn"] = strings.Join(alpn, ",")
		}
		if fp := p.Str("client-fingerprint"); fp != "" {
			m["fp"] = fp
		}
	}
	switch p.Str("network") {
	case "ws":
		m["net"] = "ws"
		if o := p.Sub("ws-opts"); o != nil {
			m["path"] = str(o["path"])
			if h, ok := o["headers"].(map[string]any); ok {
				m["host"] = str(h["Host"])
			}
		}
	case "grpc":
		m["net"] = "grpc"
		if o := p.Sub("grpc-opts"); o != nil {
			m["path"] = str(o["grpc-service-name"])
		}
	case "h2":
		m["net"] = "h2"
		if o := p.Sub("h2-opts"); o != nil {
			m["path"] = str(o["path"])
			m["host"] = strings.Join(anyList(o["host"]), ",")
		}
	case "http":
		m["net"] = "tcp"
		m["type"] = "http"
		if o := p.Sub("http-opts"); o != nil {
			m["path"] = strings.Join(anyList(o["path"]), ",")
			if h, ok := o["headers"].(map[string]any); ok {
				m["host"] = strings.Join(anyList(h["Host"]), ",")
			}
		}
	}
	b, _ := json.Marshal(m)
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

func anyList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, str(x))
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func transportQuery(p Proxy, v url.Values) {
	switch p.Str("network") {
	case "", "tcp":
		v.Set("type", "tcp")
	case "ws", "httpupgrade":
		v.Set("type", p.Str("network"))
		if o := p.Sub("ws-opts"); o != nil {
			setIf(v, "path", str(o["path"]))
			if h, ok := o["headers"].(map[string]any); ok {
				setIf(v, "host", str(h["Host"]))
			}
		}
	case "grpc":
		v.Set("type", "grpc")
		if o := p.Sub("grpc-opts"); o != nil {
			setIf(v, "serviceName", str(o["grpc-service-name"]))
		}
	case "h2":
		v.Set("type", "http")
		if o := p.Sub("h2-opts"); o != nil {
			setIf(v, "path", str(o["path"]))
			setIf(v, "host", strings.Join(anyList(o["host"]), ","))
		}
	case "http":
		v.Set("type", "tcp")
		v.Set("headerType", "http")
		if o := p.Sub("http-opts"); o != nil {
			setIf(v, "path", strings.Join(anyList(o["path"]), ","))
			if h, ok := o["headers"].(map[string]any); ok {
				setIf(v, "host", strings.Join(anyList(h["Host"]), ","))
			}
		}
	case "xhttp":
		v.Set("type", "xhttp")
		if o := p.Sub("xhttp-opts"); o != nil {
			setIf(v, "path", str(o["path"]))
			setIf(v, "host", str(o["host"]))
			setIf(v, "mode", str(o["mode"]))
		}
	default:
		v.Set("type", p.Str("network"))
	}
}

func tlsQuery(p Proxy, v url.Values, sniKey string) {
	if ro := p.Sub("reality-opts"); ro != nil {
		v.Set("security", "reality")
		setIf(v, "pbk", str(ro["public-key"]))
		setIf(v, "sid", str(ro["short-id"]))
	} else if p.Bool("tls") || sniKey == "sni" {
		v.Set("security", "tls")
	} else {
		v.Set("security", "none")
		return
	}
	setIf(v, "sni", p.Str(sniKey))
	setIf(v, "fp", p.Str("client-fingerprint"))
	if alpn := p.StrList("alpn"); len(alpn) > 0 {
		v.Set("alpn", strings.Join(alpn, ","))
	}
	if p.Bool("skip-cert-verify") {
		v.Set("allowInsecure", "1")
	}
}

func encodeVLESS(p Proxy) string {
	v := url.Values{}
	v.Set("encryption", orDefault(p.Str("encryption"), "none"))
	setIf(v, "flow", p.Str("flow"))
	tlsQuery(p, v, "servername")
	transportQuery(p, v)
	setIf(v, "packetEncoding", p.Str("packet-encoding"))
	return "vless://" + p.Str("uuid") + "@" + p.HostPort() + encodeQuery(v) + frag(p.Name)
}

func encodeTrojan(p Proxy) string {
	v := url.Values{}
	tlsQuery(p, v, "sni")
	transportQuery(p, v)
	return "trojan://" + url.PathEscape(p.Str("password")) + "@" + p.HostPort() + encodeQuery(v) + frag(p.Name)
}

func encodeHysteria2(p Proxy) string {
	v := url.Values{}
	setIf(v, "sni", p.Str("sni"))
	if p.Bool("skip-cert-verify") {
		v.Set("insecure", "1")
	}
	if obfs := p.Str("obfs"); obfs != "" {
		v.Set("obfs", obfs)
		setIf(v, "obfs-password", p.Str("obfs-password"))
	}
	if alpn := p.StrList("alpn"); len(alpn) > 0 {
		v.Set("alpn", strings.Join(alpn, ","))
	}
	setIf(v, "up", p.Str("up"))
	setIf(v, "down", p.Str("down"))
	setIf(v, "pinSHA256", p.Str("fingerprint"))
	hostport := p.HostPort()
	if ports := p.Str("ports"); ports != "" {
		setIf(v, "mport", ports)
	}
	return "hysteria2://" + url.PathEscape(p.Str("password")) + "@" + hostport + encodeQuery(v) + frag(p.Name)
}

func encodeTUIC(p Proxy) string {
	v := url.Values{}
	setIf(v, "sni", p.Str("sni"))
	if alpn := p.StrList("alpn"); len(alpn) > 0 {
		v.Set("alpn", strings.Join(alpn, ","))
	}
	setIf(v, "congestion_control", p.Str("congestion-controller"))
	setIf(v, "udp_relay_mode", p.Str("udp-relay-mode"))
	if p.Bool("skip-cert-verify") {
		v.Set("allow_insecure", "1")
	}
	if p.Bool("disable-sni") {
		v.Set("disable_sni", "1")
	}
	return "tuic://" + url.PathEscape(p.Str("uuid")) + ":" + url.PathEscape(p.Str("password")) + "@" + p.HostPort() + encodeQuery(v) + frag(p.Name)
}

func encodeAnyTLS(p Proxy) string {
	v := url.Values{}
	setIf(v, "sni", p.Str("sni"))
	if p.Bool("skip-cert-verify") {
		v.Set("insecure", "1")
	}
	setIf(v, "fp", p.Str("client-fingerprint"))
	if alpn := p.StrList("alpn"); len(alpn) > 0 {
		v.Set("alpn", strings.Join(alpn, ","))
	}
	return "anytls://" + url.PathEscape(p.Str("password")) + "@" + p.HostPort() + encodeQuery(v) + frag(p.Name)
}

func encodeSnell(p Proxy) string {
	v := url.Values{}
	v.Set("version", strconv.Itoa(orInt(p.Int("version"), 4)))
	if oo := p.Sub("obfs-opts"); oo != nil {
		setIf(v, "obfs", str(oo["mode"]))
		setIf(v, "obfs-host", str(oo["host"]))
	}
	if p.Bool("reuse") {
		v.Set("reuse", "1")
	}
	return "snell://" + url.PathEscape(p.Str("psk")) + "@" + p.HostPort() + encodeQuery(v) + frag(p.Name)
}

func orInt(n, d int) int {
	if n == 0 {
		return d
	}
	return n
}

func encodeSimple(scheme string, p Proxy) string {
	auth := ""
	if u := p.Str("username"); u != "" {
		auth = url.PathEscape(u)
		if pw := p.Str("password"); pw != "" {
			auth += ":" + url.PathEscape(pw)
		}
		auth += "@"
	}
	v := url.Values{}
	if scheme == "socks5" && p.Bool("tls") {
		v.Set("tls", "1")
	}
	return scheme + "://" + auth + p.HostPort() + encodeQuery(v) + frag(p.Name)
}
