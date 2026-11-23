package proxynode

import (
	"fmt"
	"slices"
	"strings"
)

// ParseSurgeLine parses one Surge [Proxy] line, e.g.
//
//	🇯🇵JP = snell, naiba2.xiaov.uno, 11831, psk = wWvhPWe, version = 5, reuse = true
func ParseSurgeLine(raw string) (Proxy, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "\ufeff")
	if raw == "" {
		return Proxy{}, fmt.Errorf("empty surge line")
	}
	name, rest, ok := strings.Cut(raw, "=")
	if !ok {
		return Proxy{}, fmt.Errorf("not a surge proxy line")
	}
	name = strings.TrimSpace(name)
	fields, err := splitSurgeFields(rest)
	if err != nil {
		return Proxy{}, err
	}
	if len(fields) < 3 {
		return Proxy{}, fmt.Errorf("surge: need type, server, port")
	}
	typ := strings.ToLower(strings.TrimSpace(fields[0]))
	server := strings.TrimSpace(fields[1])
	port := toInt(fields[2])
	if name == "" || server == "" || port <= 0 || port > 65535 {
		return Proxy{}, fmt.Errorf("surge: invalid name/server/port")
	}
	kv := map[string]string{}
	var extra []string
	for _, f := range fields[3:] {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			if strings.TrimSpace(f) != "" {
				extra = append(extra, strings.TrimSpace(f))
			}
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		if _, exists := kv[key]; exists {
			return Proxy{}, fmt.Errorf("Surge 节点选项重复")
		}
		kv[key] = strings.TrimSpace(v)
	}
	name = strings.Trim(name, `"`)
	p := Proxy{Name: name, Server: server, Port: port, Params: map[string]any{}}
	switch typ {
	case "snell":
		p.Type = "snell"
		if psk := firstKV(kv, "psk", "password"); psk != "" {
			p.Params["psk"] = psk
		} else if len(extra) > 0 {
			p.Params["psk"] = extra[0]
		} else {
			return p, fmt.Errorf("surge snell: missing psk")
		}
		ver := toInt(firstKV(kv, "version", "v"))
		if ver == 0 {
			ver = 4
		}
		p.Params["version"] = ver
		if ver >= 4 {
			p.Params["udp"] = true
		}
		if toBool(kv["reuse"]) {
			p.Params["reuse"] = true
		}
		if obfs := kv["obfs"]; obfs != "" && obfs != "off" && obfs != "none" {
			oo := map[string]any{"mode": obfs}
			if h := firstKV(kv, "obfs-host", "obfs-sni", "host"); h != "" {
				oo["host"] = h
			}
			p.Params["obfs-opts"] = oo
		}
	case "ss", "shadowsocks":
		p.Type = "ss"
		p.Set("cipher", firstKV(kv, "encrypt-method", "cipher"))
		if pw := kv["password"]; pw != "" {
			p.Params["password"] = pw
		} else if len(extra) > 0 {
			p.Params["password"] = extra[0]
		}
		if p.Str("cipher") == "" || p.Str("password") == "" {
			return p, fmt.Errorf("surge ss: missing cipher/password")
		}
		if obfs := kv["obfs"]; obfs != "" {
			p.Params["plugin"] = "obfs"
			oo := map[string]any{"mode": obfs}
			if h := firstKV(kv, "obfs-host", "obfs-uri"); h != "" {
				oo["host"] = h
			}
			p.Params["plugin-opts"] = oo
		}
	case "vmess":
		p.Type = "vmess"
		p.Set("uuid", firstKV(kv, "username", "uuid"))
		if p.Str("uuid") == "" && len(extra) > 0 {
			p.Params["uuid"] = extra[0]
		}
		if p.Str("uuid") == "" {
			return p, fmt.Errorf("surge vmess: missing uuid")
		}
		if toBool(kv["tls"]) {
			p.Params["tls"] = true
		}
		p.Set("servername", firstKV(kv, "sni", "peer"))
		if toBool(firstKV(kv, "skip-cert-verify", "insecure")) {
			p.Params["skip-cert-verify"] = true
		}
		if toBool(kv["ws"]) {
			p.Params["network"] = "ws"
			wo := map[string]any{}
			if path := firstKV(kv, "ws-path", "ws-uri"); path != "" {
				wo["path"] = path
			}
			if h := firstKV(kv, "ws-headers", "host"); h != "" {
				wo["headers"] = map[string]any{"Host": strings.TrimPrefix(h, "Host:")}
			}
			if len(wo) > 0 {
				p.Params["ws-opts"] = wo
			}
		}
	case "vless":
		p.Type = "vless"
		if len(extra) > 0 {
			return p, unsupported("VLESS 行含未知位置参数")
		}
		if err := parseNativeVLESS(&p, kv); err != nil {
			return p, err
		}
	case "trojan":
		p.Type = "trojan"
		if pw := kv["password"]; pw != "" {
			p.Params["password"] = pw
		} else if len(extra) > 0 {
			p.Params["password"] = extra[0]
		}
		if p.Str("password") == "" {
			return p, fmt.Errorf("surge trojan: missing password")
		}
		if err := nativeKeys(kv, "password sni peer skip-cert-verify insecure allow-insecure ws ws-path ws-headers alpn tfo"); err != nil {
			return p, err
		}
		if err := consistentAliases(kv, "sni peer", "skip-cert-verify insecure allow-insecure"); err != nil {
			return p, err
		}
		if err := nativeBools(kv, "skip-cert-verify", "insecure", "allow-insecure", "ws", "tfo"); err != nil {
			return p, err
		}
		p.Set("sni", firstKV(kv, "sni", "peer"))
		if toBool(firstKV(kv, "skip-cert-verify", "insecure", "allow-insecure")) {
			p.Params["skip-cert-verify"] = true
		}
		if toBool(kv["tfo"]) {
			p.Params["tfo"] = true
		}
		if a := kv["alpn"]; a != "" {
			p.Params["alpn"] = splitList(a)
		}
		if toBool(kv["ws"]) {
			p.Params["network"] = "ws"
			if err := nativeWS(&p, kv["ws-path"], kv["ws-headers"], ""); err != nil {
				return p, err
			}
		} else if firstKV(kv, "ws-path", "ws-headers") != "" {
			return p, unsupported("未启用 WebSocket 的节点带有 WebSocket 选项")
		}
	case "hysteria2", "hy2":
		p.Type = "hysteria2"
		if pw := firstKV(kv, "password", "auth"); pw != "" {
			p.Params["password"] = pw
		}
		p.Set("sni", firstKV(kv, "sni", "peer"))
		if toBool(firstKV(kv, "skip-cert-verify", "insecure")) {
			p.Params["skip-cert-verify"] = true
		}
		if bw := firstKV(kv, "download-bandwidth", "down"); bw != "" {
			p.Params["down"] = strings.TrimSpace(bw) + " Mbps"
		}
		p.Set("ports", kv["port-hopping"])
	case "tuic", "tuic-v5":
		p.Type = "tuic"
		p.Set("uuid", kv["uuid"])
		p.Set("password", kv["password"])
		p.Set("sni", firstKV(kv, "sni", "peer"))
		if toBool(firstKV(kv, "skip-cert-verify", "insecure")) {
			p.Params["skip-cert-verify"] = true
		}
	case "anytls":
		p.Type = "anytls"
		p.Set("password", kv["password"])
		p.Set("sni", firstKV(kv, "sni", "peer"))
		if toBool(firstKV(kv, "skip-cert-verify", "insecure")) {
			p.Params["skip-cert-verify"] = true
		}
	case "socks5", "socks5-tls":
		p.Type = "socks5"
		if typ == "socks5-tls" {
			p.Params["tls"] = true
		}
		if len(extra) >= 2 {
			p.Params["username"], p.Params["password"] = extra[0], extra[1]
		}
		p.Set("username", kv["username"])
		p.Set("password", kv["password"])
	case "http", "https":
		p.Type = "http"
		if typ == "https" {
			p.Params["tls"] = true
		}
		if len(extra) >= 2 {
			p.Params["username"], p.Params["password"] = extra[0], extra[1]
		}
		p.Set("username", kv["username"])
		p.Set("password", kv["password"])
	default:
		return p, fmt.Errorf("surge: unsupported type %s", typ)
	}
	if err := consistentAliases(kv, "underlying-proxy dialer-proxy"); err != nil {
		return p, err
	}
	if via := firstKV(kv, "underlying-proxy", "dialer-proxy"); via != "" {
		p.Params["dialer-proxy"] = via
	}
	if p.Type == "vless" || p.Type == "trojan" {
		if err := validateProxy(p); err != nil {
			return Proxy{}, err
		}
	}
	return p, nil
}

// Reject unmodelled native options instead of returning a plausible TCP node.
func nativeKeys(kv map[string]string, allowed string) error {
	keys := strings.Fields(allowed + " underlying-proxy dialer-proxy test-timeout")
	for k := range kv {
		if !slices.Contains(keys, k) {
			return unsupported("原生节点行含不能无损转换的选项")
		}
	}
	return nil
}

func consistentAliases(kv map[string]string, groups ...string) error {
	for _, group := range groups {
		var first string
		set := false
		for _, k := range strings.Fields(group) {
			if v, ok := kv[k]; ok {
				if set && v != first {
					return unsupported("同一节点选项的多个拼写互相冲突")
				}
				first, set = v, true
			}
		}
	}
	return nil
}

func nativeBools(kv map[string]string, keys ...string) error {
	for _, k := range keys {
		if v, ok := kv[k]; ok {
			if _, err := strictBool(v); err != nil {
				return err
			}
		}
	}
	return nil
}

func nativeWS(p *Proxy, path, headers, host string) error {
	if headers != "" {
		key, value, ok := strings.Cut(headers, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Host") || strings.ContainsAny(value, "|;\r\n") || host != "" {
			return unsupported("仅支持单个 WebSocket Host 请求头")
		}
		host = strings.TrimSpace(value)
	}
	o := map[string]any{}
	if path != "" {
		o["path"] = path
	}
	if host != "" {
		o["headers"] = map[string]any{"Host": host}
	}
	p.Params["ws-opts"] = o
	return nil
}

func parseNativeVLESS(p *Proxy, kv map[string]string) error {
	if err := nativeKeys(kv, "username uuid password tls sni peer reality reality-public-key pbk reality-short-id sid encryption encrypt-method flow block-quic client-fingerprint fp alpn udp-relay tfo ws ws-path ws-uri ws-headers ws-http-upgrade obfs obfs-host obfs-uri xhttp xhttp-path xhttp-host xhttp-mode grpc grpc-service-name grpc-mode grpc-authority skip-cert-verify allow-insecure insecure"); err != nil {
		return err
	}
	if err := consistentAliases(kv, "username uuid password", "sni peer", "encryption encrypt-method", "reality-public-key pbk", "reality-short-id sid", "client-fingerprint fp", "ws-path ws-uri obfs-uri", "skip-cert-verify insecure allow-insecure"); err != nil {
		return err
	}
	if err := nativeBools(kv, "tls", "reality", "ws", "ws-http-upgrade", "xhttp", "grpc", "tfo", "udp-relay", "skip-cert-verify", "allow-insecure", "insecure"); err != nil {
		return err
	}
	p.Set("uuid", firstKV(kv, "username", "uuid", "password"))
	if p.Str("uuid") == "" {
		return fmt.Errorf("VLESS 缺少 UUID")
	}
	p.Set("servername", firstKV(kv, "sni", "peer"))
	p.Set("client-fingerprint", firstKV(kv, "client-fingerprint", "fp"))
	p.Set("flow", kv["flow"])
	if enc := firstKV(kv, "encryption", "encrypt-method"); enc != "none" {
		p.Set("encryption", enc)
	}
	pk := firstKV(kv, "reality-public-key", "pbk")
	sid := firstKV(kv, "reality-short-id", "sid")
	if pk != "" {
		if p.Str("servername") == "" || kv["reality"] != "" && !toBool(kv["reality"]) || kv["tls"] != "" && !toBool(kv["tls"]) {
			return unsupported("REALITY 缺少 SNI 或与显式选项冲突")
		}
		ro := map[string]any{"public-key": pk}
		if sid != "" {
			ro["short-id"] = sid
		}
		p.Params["reality-opts"] = ro
	} else if toBool(kv["reality"]) || sid != "" {
		return unsupported("REALITY 缺少公钥")
	}
	p.Set("tls", pk != "" || toBool(kv["tls"]))
	if alpn := kv["alpn"]; alpn != "" {
		p.Params["alpn"] = splitList(alpn)
	}
	if !p.Bool("tls") && (p.Str("servername") != "" || p.Str("client-fingerprint") != "" || kv["alpn"] != "") {
		return unsupported("明文 VLESS 不能包含 TLS 握手参数")
	}
	if toBool(firstKV(kv, "skip-cert-verify", "insecure", "allow-insecure")) {
		if !p.Bool("tls") || pk != "" {
			return unsupported("当前安全层不能设置跳过证书验证")
		}
		p.Params["skip-cert-verify"] = true
	}
	if v, ok := kv["udp-relay"]; ok {
		p.Params["udp"] = toBool(v)
	}
	p.Set("tfo", toBool(kv["tfo"]))
	if v, ok := kv["block-quic"]; ok {
		block, err := strictBool(v)
		flow := p.Str("flow")
		if err != nil || !((block && flow == "xtls-rprx-vision") || (!block && flow == "xtls-rprx-vision-udp443")) {
			return unsupported("无法保留独立或冲突的 QUIC 策略")
		}
	}
	network := ""
	for _, key := range []string{"ws", "xhttp", "grpc"} {
		if toBool(kv[key]) {
			if network != "" {
				return unsupported("VLESS 传输选项冲突")
			}
			network = key
		}
	}
	if obfs := kv["obfs"]; obfs != "" && obfs != "none" {
		if obfs != "ws" || network != "" && network != obfs {
			return unsupported("不支持此原生 VLESS obfs")
		}
		network = obfs
	}
	if network != "ws" && (toBool(kv["ws-http-upgrade"]) || firstKV(kv, "ws-path", "ws-uri", "ws-headers", "obfs-uri", "obfs-host") != "") || network != "grpc" && firstKV(kv, "grpc-mode", "grpc-authority", "grpc-service-name") != "" || network != "xhttp" && firstKV(kv, "xhttp-path", "xhttp-host", "xhttp-mode") != "" {
		return unsupported("传输未启用但指定了专属选项")
	}
	if flow := p.Str("flow"); flow != "" {
		if flow != "xtls-rprx-vision" && flow != "xtls-rprx-vision-udp443" {
			return unsupported("未知 VLESS flow")
		}
		if network == "grpc" || p.Str("encryption") == "" && (network != "" || !p.Bool("tls")) {
			return unsupported("此 Vision 组合需要 RAW TLS/REALITY 或明确的 VLESS Encryption")
		}
	}
	p.Set("network", network)
	switch network {
	case "ws":
		if toBool(kv["ws-http-upgrade"]) {
			p.Params["network"] = "httpupgrade"
		}
		return nativeWS(p, firstKV(kv, "ws-path", "ws-uri", "obfs-uri"), kv["ws-headers"], kv["obfs-host"])
	case "grpc":
		if !p.Bool("tls") || p.Str("flow") != "" || kv["grpc-mode"] != "" && kv["grpc-mode"] != "gun" || kv["grpc-authority"] != "" {
			return unsupported("不能无损表示此 gRPC 组合")
		}
		p.Params["grpc-opts"] = map[string]any{"grpc-service-name": kv["grpc-service-name"]}
	case "xhttp":
		o := map[string]any{}
		for _, key := range []string{"path", "host", "mode"} {
			if v := kv["xhttp-"+key]; v != "" {
				o[key] = v
			}
		}
		p.Params["xhttp-opts"] = o
	}
	return nil
}

func looksLikeSurgeLine(s string) bool {
	name, rest, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(name) == "" || strings.Contains(name, "://") {
		return false
	}
	typ, _, _ := strings.Cut(strings.TrimSpace(rest), ",")
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "snell", "ss", "shadowsocks", "vmess", "vless", "trojan",
		"hysteria2", "hy2", "tuic", "tuic-v5", "anytls",
		"socks5", "socks5-tls", "http", "https":
		return true
	}
	return false
}

// Surge encloses values containing commas (including underlying-proxy) in
// double quotes. Do not split such credentials or chain references in half.
func splitSurgeFields(s string) ([]string, error) {
	var out []string
	var value strings.Builder
	quoted := false
	flush := func() {
		if field := strings.TrimSpace(value.String()); field != "" {
			out = append(out, field)
		}
		value.Reset()
	}
	for _, ch := range s {
		switch {
		case ch == '"':
			quoted = !quoted
		case ch == ',' && !quoted:
			flush()
		default:
			value.WriteRune(ch)
		}
	}
	if quoted {
		return nil, fmt.Errorf("Surge 节点字段引号未闭合")
	}
	flush()
	return out, nil
}

func firstKV(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := kv[k]; v != "" {
			return v
		}
	}
	return ""
}
