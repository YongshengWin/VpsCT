package proxynode

import (
	"fmt"
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
	fields := splitSurgeFields(rest)
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
		kv[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
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
		p.Set("uuid", firstKV(kv, "username", "uuid"))
		if p.Str("uuid") == "" {
			return p, fmt.Errorf("surge vless: missing uuid")
		}
		if toBool(kv["tls"]) || kv["reality-public-key"] != "" {
			p.Params["tls"] = true
		}
		p.Set("servername", firstKV(kv, "sni", "peer"))
		if toBool(firstKV(kv, "skip-cert-verify", "insecure")) {
			p.Params["skip-cert-verify"] = true
		}
		if pk := kv["reality-public-key"]; pk != "" {
			ro := map[string]any{"public-key": pk}
			if sid := kv["reality-short-id"]; sid != "" {
				ro["short-id"] = sid
			}
			p.Params["reality-opts"] = ro
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
		p.Set("sni", firstKV(kv, "sni", "peer"))
		if toBool(firstKV(kv, "skip-cert-verify", "insecure")) {
			p.Params["skip-cert-verify"] = true
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
	if via := firstKV(kv, "underlying-proxy", "dialer-proxy"); via != "" {
		p.Params["dialer-proxy"] = via
	}
	return p, nil
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

func splitSurgeFields(s string) []string {
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstKV(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := kv[k]; v != "" {
			return v
		}
	}
	return ""
}
