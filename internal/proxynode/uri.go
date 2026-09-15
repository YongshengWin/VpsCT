package proxynode

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ErrUnsupported is returned for unknown URI schemes.
var ErrUnsupported = errors.New("unsupported share link")

// ParseURI parses one share link (ss://, vmess://, vless://, trojan://,
// hysteria2://, hy2://, tuic://, anytls://, snell://, socks5://, http://).
func ParseURI(raw string) (Proxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Proxy{}, errors.New("empty link")
	}
	if !strings.Contains(raw, "://") {
		if looksLikeSurgeLine(raw) {
			return ParseSurgeLine(raw)
		}
		return Proxy{}, ErrUnsupported
	}
	idx := strings.Index(raw, "://")
	if idx < 0 {
		return Proxy{}, ErrUnsupported
	}
	scheme := strings.ToLower(raw[:idx])
	switch scheme {
	case "ss":
		return parseSS(raw)
	case "vmess":
		return parseVMess(raw)
	case "vless":
		return parseVLESS(raw)
	case "trojan":
		return parseTrojan(raw)
	case "hysteria2", "hy2":
		return parseHysteria2(raw)
	case "tuic":
		return parseTUIC(raw)
	case "anytls":
		return parseAnyTLS(raw)
	case "snell":
		return parseSnell(raw)
	case "socks5", "socks", "socks5h":
		return parseSocks(raw)
	case "http", "https":
		return parseHTTP(raw)
	}
	return Proxy{}, fmt.Errorf("%w: %s", ErrUnsupported, scheme)
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "=")
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func fragmentName(u *url.URL, fallback string) string {
	if u.Fragment != "" {
		if n, err := url.QueryUnescape(u.Fragment); err == nil {
			return strings.TrimSpace(n)
		}
		return u.Fragment
	}
	return fallback
}

func hostPort(u *url.URL) (string, int, error) {
	host := u.Hostname()
	portStr := u.Port()
	if host == "" {
		return "", 0, errors.New("missing host")
	}
	if portStr == "" {
		if u.Scheme == "https" {
			return host, 443, nil
		}
		if u.Scheme == "http" {
			return host, 80, nil
		}
		return "", 0, errors.New("missing port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("bad port %q", portStr)
	}
	return host, port, nil
}

func q(u *url.URL, keys ...string) string {
	for _, k := range keys {
		if v := u.Query().Get(k); v != "" {
			return v
		}
	}
	return ""
}

func parseSS(raw string) (Proxy, error) {
	p := Proxy{Type: "ss", Params: map[string]any{}}
	body := raw[len("ss://"):]
	frag := ""
	if i := strings.Index(body, "#"); i >= 0 {
		frag, body = body[i+1:], body[:i]
	}
	query := ""
	if i := strings.Index(body, "?"); i >= 0 {
		query, body = body[i+1:], body[:i]
	}
	var userinfo, hostport string
	if at := strings.LastIndex(body, "@"); at >= 0 {
		userinfo, hostport = body[:at], body[at+1:]
		// SIP002: userinfo may be base64(method:password) or method:password (2022)
		if dec, err := decodeB64(userinfo); err == nil && strings.Contains(string(dec), ":") {
			userinfo = string(dec)
		} else if u, err := url.PathUnescape(userinfo); err == nil {
			userinfo = u
		}
	} else {
		// legacy: whole body is base64(method:password@host:port)
		dec, err := decodeB64(body)
		if err != nil {
			return p, errors.New("ss: cannot decode legacy link")
		}
		s := string(dec)
		at := strings.LastIndex(s, "@")
		if at < 0 {
			return p, errors.New("ss: malformed legacy link")
		}
		userinfo, hostport = s[:at], s[at+1:]
	}
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return p, errors.New("ss: missing method:password")
	}
	p.Params["cipher"] = userinfo[:colon]
	p.Params["password"] = userinfo[colon+1:]
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return p, fmt.Errorf("ss: %v", err)
	}
	p.Server = host
	p.Port, _ = strconv.Atoi(portStr)
	if p.Port == 0 {
		return p, errors.New("ss: bad port")
	}
	if query != "" {
		vals, _ := url.ParseQuery(query)
		if plugin := vals.Get("plugin"); plugin != "" {
			parts := strings.Split(plugin, ";")
			name := parts[0]
			opts := map[string]string{}
			for _, kv := range parts[1:] {
				k, v, _ := strings.Cut(kv, "=")
				opts[k] = v
			}
			switch name {
			case "obfs-local", "simple-obfs", "obfs":
				p.Params["plugin"] = "obfs"
				po := map[string]any{"mode": opts["obfs"]}
				if h, ok := opts["obfs-host"]; ok {
					po["host"] = h
				}
				p.Params["plugin-opts"] = po
			case "v2ray-plugin":
				p.Params["plugin"] = "v2ray-plugin"
				po := map[string]any{"mode": "websocket"}
				if _, ok := opts["tls"]; ok {
					po["tls"] = true
				}
				if h, ok := opts["host"]; ok {
					po["host"] = h
				}
				if pa, ok := opts["path"]; ok {
					po["path"] = pa
				}
				p.Params["plugin-opts"] = po
			case "shadow-tls":
				p.Params["plugin"] = "shadow-tls"
				po := map[string]any{}
				if h, ok := opts["host"]; ok {
					po["host"] = h
				}
				if pw, ok := opts["password"]; ok {
					po["password"] = pw
				}
				if v, ok := opts["version"]; ok {
					po["version"], _ = strconv.Atoi(v)
				}
				p.Params["plugin-opts"] = po
			}
		}
		if vals.Get("udp-over-tcp") == "true" || vals.Get("uot") == "1" {
			p.Params["udp-over-tcp"] = true
		}
	}
	p.Params["udp"] = true
	if frag != "" {
		if n, err := url.QueryUnescape(frag); err == nil {
			p.Name = strings.TrimSpace(n)
		} else {
			p.Name = frag
		}
	}
	if p.Name == "" {
		p.Name = p.HostPort()
	}
	return p, nil
}

type vmessJSON struct {
	V    any    `json:"v"`
	PS   string `json:"ps"`
	Add  string `json:"add"`
	Port any    `json:"port"`
	ID   string `json:"id"`
	Aid  any    `json:"aid"`
	Scy  string `json:"scy"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni"`
	Alpn string `json:"alpn"`
	Fp   string `json:"fp"`
}

func parseVMess(raw string) (Proxy, error) {
	p := Proxy{Type: "vmess", Params: map[string]any{}}
	body := raw[len("vmess://"):]
	dec, err := decodeB64(body)
	if err != nil {
		return p, errors.New("vmess: link is not base64 json")
	}
	var v vmessJSON
	if err := json.Unmarshal(dec, &v); err != nil {
		return p, fmt.Errorf("vmess: %v", err)
	}
	p.Name = v.PS
	p.Server = v.Add
	p.Port = toInt(v.Port)
	if p.Server == "" || p.Port == 0 || v.ID == "" {
		return p, errors.New("vmess: missing add/port/id")
	}
	p.Params["uuid"] = v.ID
	p.Params["alterId"] = toInt(v.Aid)
	cipher := v.Scy
	if cipher == "" {
		cipher = "auto"
	}
	p.Params["cipher"] = cipher
	p.Params["udp"] = true
	if v.TLS == "tls" {
		p.Params["tls"] = true
		p.Set("servername", v.SNI)
		if v.Alpn != "" {
			p.Params["alpn"] = strings.Split(v.Alpn, ",")
		}
		p.Set("client-fingerprint", v.Fp)
	}
	net := v.Net
	if net == "" {
		net = "tcp"
	}
	switch net {
	case "ws":
		p.Params["network"] = "ws"
		opts := map[string]any{}
		if v.Path != "" {
			opts["path"] = v.Path
		}
		if v.Host != "" {
			opts["headers"] = map[string]any{"Host": v.Host}
		}
		p.Params["ws-opts"] = opts
	case "grpc":
		p.Params["network"] = "grpc"
		p.Params["grpc-opts"] = map[string]any{"grpc-service-name": v.Path}
	case "h2":
		p.Params["network"] = "h2"
		opts := map[string]any{"path": v.Path}
		if v.Host != "" {
			opts["host"] = strings.Split(v.Host, ",")
		}
		p.Params["h2-opts"] = opts
	case "tcp":
		if v.Type == "http" {
			p.Params["network"] = "http"
			opts := map[string]any{}
			if v.Path != "" {
				opts["path"] = strings.Split(v.Path, ",")
			}
			if v.Host != "" {
				opts["headers"] = map[string]any{"Host": strings.Split(v.Host, ",")}
			}
			p.Params["http-opts"] = opts
		}
	default:
		p.Params["network"] = net
	}
	if p.Name == "" {
		p.Name = p.HostPort()
	}
	return p, nil
}

// applyTransport maps the shared type/path/host/serviceName query params of
// vless/trojan links onto Clash network options.
func applyTransport(p *Proxy, u *url.URL) {
	network := q(u, "type")
	switch network {
	case "", "tcp":
		if q(u, "headerType") == "http" {
			p.Params["network"] = "http"
			opts := map[string]any{}
			if pa := q(u, "path"); pa != "" {
				opts["path"] = strings.Split(pa, ",")
			}
			if h := q(u, "host"); h != "" {
				opts["headers"] = map[string]any{"Host": strings.Split(h, ",")}
			}
			p.Params["http-opts"] = opts
		}
	case "ws", "httpupgrade":
		p.Params["network"] = network
		opts := map[string]any{}
		if pa := q(u, "path"); pa != "" {
			opts["path"] = pa
		}
		if h := q(u, "host"); h != "" {
			opts["headers"] = map[string]any{"Host": h}
		}
		if ed := q(u, "ed"); ed != "" {
			opts["max-early-data"], _ = strconv.Atoi(ed)
			opts["early-data-header-name"] = "Sec-WebSocket-Protocol"
		}
		p.Params["ws-opts"] = opts
	case "grpc":
		p.Params["network"] = "grpc"
		p.Params["grpc-opts"] = map[string]any{"grpc-service-name": q(u, "serviceName")}
	case "h2", "http":
		p.Params["network"] = "h2"
		opts := map[string]any{}
		if pa := q(u, "path"); pa != "" {
			opts["path"] = pa
		}
		if h := q(u, "host"); h != "" {
			opts["host"] = strings.Split(h, ",")
		}
		p.Params["h2-opts"] = opts
	case "xhttp", "splithttp":
		p.Params["network"] = "xhttp"
		opts := map[string]any{}
		if pa := q(u, "path"); pa != "" {
			opts["path"] = pa
		}
		if h := q(u, "host"); h != "" {
			opts["host"] = h
		}
		if m := q(u, "mode"); m != "" {
			opts["mode"] = m
		}
		p.Params["xhttp-opts"] = opts
	default:
		p.Params["network"] = network
	}
}

func applyTLS(p *Proxy, u *url.URL, defaultOn bool) {
	sec := q(u, "security")
	switch sec {
	case "tls", "xtls":
		p.Params["tls"] = true
	case "reality":
		p.Params["tls"] = true
		ro := map[string]any{"public-key": q(u, "pbk")}
		if sid := q(u, "sid"); sid != "" {
			ro["short-id"] = sid
		}
		p.Params["reality-opts"] = ro
	case "none":
	case "":
		if defaultOn {
			p.Params["tls"] = true
		}
	}
	if toBool(p.Params["tls"]) {
		p.Set("servername", q(u, "sni", "peer"))
		p.Set("client-fingerprint", q(u, "fp"))
		if alpn := q(u, "alpn"); alpn != "" {
			p.Params["alpn"] = strings.Split(alpn, ",")
		}
		if toBool(q(u, "allowInsecure", "insecure", "skip-cert-verify")) {
			p.Params["skip-cert-verify"] = true
		}
	}
}

func parseVLESS(raw string) (Proxy, error) {
	p := Proxy{Type: "vless", Params: map[string]any{}}
	u, err := url.Parse(raw)
	if err != nil {
		return p, fmt.Errorf("vless: %v", err)
	}
	p.Server, p.Port, err = hostPort(u)
	if err != nil {
		return p, fmt.Errorf("vless: %v", err)
	}
	uuid := u.User.Username()
	if uuid == "" {
		return p, errors.New("vless: missing uuid")
	}
	p.Params["uuid"] = uuid
	p.Params["udp"] = true
	p.Set("flow", q(u, "flow"))
	if enc := q(u, "encryption"); enc != "" && enc != "none" {
		p.Params["encryption"] = enc
	}
	applyTLS(&p, u, false)
	applyTransport(&p, u)
	if pf := q(u, "packetEncoding", "packet-encoding"); pf != "" {
		p.Params["packet-encoding"] = pf
	}
	p.Name = fragmentName(u, p.HostPort())
	return p, nil
}

func parseTrojan(raw string) (Proxy, error) {
	p := Proxy{Type: "trojan", Params: map[string]any{}}
	u, err := url.Parse(raw)
	if err != nil {
		return p, fmt.Errorf("trojan: %v", err)
	}
	p.Server, p.Port, err = hostPort(u)
	if err != nil {
		return p, fmt.Errorf("trojan: %v", err)
	}
	pw := u.User.Username()
	if pass, ok := u.User.Password(); ok && pass != "" {
		pw = pw + ":" + pass
	}
	if pw == "" {
		return p, errors.New("trojan: missing password")
	}
	p.Params["password"] = pw
	p.Params["udp"] = true
	applyTLS(&p, u, true)
	// trojan always TLS: mihomo uses sni instead of servername
	if sn, ok := p.Params["servername"]; ok {
		p.Params["sni"] = sn
		delete(p.Params, "servername")
	}
	delete(p.Params, "tls")
	applyTransport(&p, u)
	p.Name = fragmentName(u, p.HostPort())
	return p, nil
}

func parseHysteria2(raw string) (Proxy, error) {
	p := Proxy{Type: "hysteria2", Params: map[string]any{}}
	raw = "hysteria2://" + raw[strings.Index(raw, "://")+3:]
	u, err := url.Parse(raw)
	if err != nil {
		return p, fmt.Errorf("hysteria2: %v", err)
	}
	host := u.Hostname()
	portStr := u.Port()
	// port hopping: host:2000-3000 or host:2000,3000 – url.Parse rejects, so fall back
	if host == "" {
		return p, errors.New("hysteria2: missing host")
	}
	if portStr == "" {
		// url.Parse fails on ranges; try manual extraction
		hp := u.Host
		if i := strings.LastIndex(hp, ":"); i >= 0 {
			portStr = hp[i+1:]
		}
	}
	if strings.ContainsAny(portStr, "-,") {
		p.Params["ports"] = portStr
		first := strings.FieldsFunc(portStr, func(r rune) bool { return r == '-' || r == ',' })[0]
		p.Port, _ = strconv.Atoi(first)
	} else {
		p.Port, _ = strconv.Atoi(portStr)
	}
	if p.Port == 0 {
		p.Port = 443
	}
	p.Server = host
	pw := u.User.Username()
	if pass, ok := u.User.Password(); ok && pass != "" {
		pw = pw + ":" + pass
	}
	p.Params["password"] = pw
	p.Set("sni", q(u, "sni", "peer"))
	if toBool(q(u, "insecure", "allowInsecure")) {
		p.Params["skip-cert-verify"] = true
	}
	if obfs := q(u, "obfs"); obfs != "" && obfs != "none" {
		p.Params["obfs"] = obfs
		p.Set("obfs-password", q(u, "obfs-password", "obfsParam"))
	}
	if alpn := q(u, "alpn"); alpn != "" {
		p.Params["alpn"] = strings.Split(alpn, ",")
	}
	p.Set("up", q(u, "up", "upmbps"))
	p.Set("down", q(u, "down", "downmbps"))
	if pin := q(u, "pinSHA256"); pin != "" {
		p.Params["fingerprint"] = pin
	}
	if mp := q(u, "mport"); mp != "" {
		p.Params["ports"] = mp
	}
	p.Name = fragmentName(u, p.HostPort())
	return p, nil
}

func parseTUIC(raw string) (Proxy, error) {
	p := Proxy{Type: "tuic", Params: map[string]any{}}
	u, err := url.Parse(raw)
	if err != nil {
		return p, fmt.Errorf("tuic: %v", err)
	}
	p.Server, p.Port, err = hostPort(u)
	if err != nil {
		return p, fmt.Errorf("tuic: %v", err)
	}
	uuid := u.User.Username()
	pw, _ := u.User.Password()
	if uuid == "" {
		return p, errors.New("tuic: missing uuid")
	}
	p.Params["uuid"] = uuid
	p.Params["password"] = pw
	p.Set("sni", q(u, "sni", "peer"))
	if alpn := q(u, "alpn"); alpn != "" {
		p.Params["alpn"] = strings.Split(alpn, ",")
	} else {
		p.Params["alpn"] = []string{"h3"}
	}
	cc := q(u, "congestion_control", "congestion-controller", "congestion_controller")
	if cc == "" {
		cc = "bbr"
	}
	p.Params["congestion-controller"] = cc
	mode := q(u, "udp_relay_mode", "udp-relay-mode")
	if mode == "" {
		mode = "native"
	}
	p.Params["udp-relay-mode"] = mode
	if toBool(q(u, "allow_insecure", "allowInsecure", "insecure")) {
		p.Params["skip-cert-verify"] = true
	}
	if toBool(q(u, "disable_sni")) {
		p.Params["disable-sni"] = true
	}
	if toBool(q(u, "reduce_rtt")) {
		p.Params["reduce-rtt"] = true
	}
	p.Name = fragmentName(u, p.HostPort())
	return p, nil
}

func parseAnyTLS(raw string) (Proxy, error) {
	p := Proxy{Type: "anytls", Params: map[string]any{}}
	u, err := url.Parse(raw)
	if err != nil {
		return p, fmt.Errorf("anytls: %v", err)
	}
	p.Server, p.Port, err = hostPort(u)
	if err != nil {
		return p, fmt.Errorf("anytls: %v", err)
	}
	pw := u.User.Username()
	if pass, ok := u.User.Password(); ok && pass != "" {
		pw = pw + ":" + pass
	}
	if pw == "" {
		return p, errors.New("anytls: missing password")
	}
	p.Params["password"] = pw
	p.Params["udp"] = true
	p.Set("sni", q(u, "sni", "peer"))
	if toBool(q(u, "insecure", "allowInsecure")) {
		p.Params["skip-cert-verify"] = true
	}
	p.Set("client-fingerprint", q(u, "fp"))
	if alpn := q(u, "alpn"); alpn != "" {
		p.Params["alpn"] = strings.Split(alpn, ",")
	}
	p.Name = fragmentName(u, p.HostPort())
	return p, nil
}

func parseSnell(raw string) (Proxy, error) {
	p := Proxy{Type: "snell", Params: map[string]any{}}
	u, err := url.Parse(raw)
	if err != nil {
		return p, fmt.Errorf("snell: %v", err)
	}
	p.Server, p.Port, err = hostPort(u)
	if err != nil {
		return p, fmt.Errorf("snell: %v", err)
	}
	psk := u.User.Username()
	if psk == "" {
		psk = q(u, "psk")
	}
	if psk == "" {
		return p, errors.New("snell: missing psk")
	}
	p.Params["psk"] = psk
	ver := toInt(q(u, "version", "v"))
	if ver == 0 {
		ver = 4
	}
	p.Params["version"] = ver
	if obfs := q(u, "obfs"); obfs != "" && obfs != "off" && obfs != "none" {
		oo := map[string]any{"mode": obfs}
		if h := q(u, "obfs-host", "host"); h != "" {
			oo["host"] = h
		}
		p.Params["obfs-opts"] = oo
	}
	if toBool(q(u, "udp")) || ver >= 4 {
		p.Params["udp"] = true
	}
	if toBool(q(u, "reuse")) {
		p.Params["reuse"] = true
	}
	p.Name = fragmentName(u, p.HostPort())
	return p, nil
}

func parseSocks(raw string) (Proxy, error) {
	p := Proxy{Type: "socks5", Params: map[string]any{}}
	u, err := url.Parse(raw)
	if err != nil {
		return p, fmt.Errorf("socks5: %v", err)
	}
	p.Server, p.Port, err = hostPort(u)
	if err != nil {
		return p, fmt.Errorf("socks5: %v", err)
	}
	if u.User != nil {
		p.Set("username", u.User.Username())
		pw, _ := u.User.Password()
		p.Set("password", pw)
	}
	if toBool(q(u, "tls")) {
		p.Params["tls"] = true
	}
	p.Params["udp"] = true
	p.Name = fragmentName(u, p.HostPort())
	return p, nil
}

func parseHTTP(raw string) (Proxy, error) {
	p := Proxy{Type: "http", Params: map[string]any{}}
	u, err := url.Parse(raw)
	if err != nil {
		return p, fmt.Errorf("http: %v", err)
	}
	p.Server, p.Port, err = hostPort(u)
	if err != nil {
		return p, fmt.Errorf("http: %v", err)
	}
	if u.User != nil {
		p.Set("username", u.User.Username())
		pw, _ := u.User.Password()
		p.Set("password", pw)
	}
	if u.Scheme == "https" {
		p.Params["tls"] = true
		p.Set("sni", q(u, "sni"))
	}
	p.Name = fragmentName(u, p.HostPort())
	return p, nil
}
