package subscription

import (
	"fmt"
	"strconv"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
)

func surgeQuote(s string) string {
	s = strings.NewReplacer("\n", " ", "\r", " ", "\x00", "").Replace(s)
	return `"` + strings.ReplaceAll(s, `"`, `'`) + `"`
}

func surgeIdent(name string) string {
	name = strings.NewReplacer("\n", " ", "\r", " ", "\x00", "").Replace(name)
	if name == "" {
		return name
	}
	if strings.ContainsAny(name, ",=\"") {
		return surgeQuote(name)
	}
	return name
}

// Surge uses ws-headers for the HTTP Host override, independently of TLS SNI.
func addSurgeWS(p proxynode.Proxy, add func(string, string)) bool {
	path, host, ok := p.BasicWebSocket()
	if !ok || strings.ContainsAny(host, "|;\r\n\x00\"") || strings.ContainsAny(path, "\r\n\x00\"") {
		return false
	}
	add("ws", "true")
	add("ws-path", path)
	if host != "" {
		add("ws-headers", "Host:"+host)
	}
	return true
}

// SurgeProxyLine renders one Surge [Proxy] line ("" when unsupported).
func SurgeProxyLine(p proxynode.Proxy, via string) string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			if strings.ContainsAny(v, ",\"\r\n") {
				v = surgeQuote(v)
			}
			parts = append(parts, k+"="+v)
		}
	}
	addBool := func(k string, v bool) {
		if v {
			parts = append(parts, k+"=true")
		}
	}
	if oneOf(p.Type, "vmess", "trojan") && !oneOf(p.Str("network"), "", "tcp", "ws") || p.Type == "tuic" && (!isTUICv5(p) || p.Bool("disable-sni")) || p.Type == "ss" && (!oneOf(p.Str("plugin"), "", "obfs") || p.Bool("udp-over-tcp")) {
		return ""
	}
	if ambiguousSimpleTLS(p) || surgeOnlyFingerprint(p) || oneOf(p.Type, "socks5", "http") && p.Params["headers"] != nil || p.Type == "socks5" && p.Bool("remote-dns-resolve") || p.Type == "vmess" && (p.Int("alterId") != 0 || p.Int("alter-id") != 0) {
		return ""
	}
	if oneOf(p.Type, "http", "socks5") && (p.Str("sni") != "" || p.Str("servername") != "" || p.Bool("skip-cert-verify") || len(p.StrList("alpn")) != 0) {
		return ""
	}
	if p.Str("fingerprint") != "" || p.Type == "hysteria2" && (p.Str("obfs") != "" || p.Str("obfs-password") != "" || via != "" && p.Str("ports") != "") {
		return ""
	}
	host, port := p.Server, strconv.Itoa(p.Port)
	switch p.Type {
	case "ss":
		parts = append(parts, "ss", host, port)
		add("encrypt-method", p.Str("cipher"))
		add("password", p.Str("password"))
		if udp, exists := p.Params["udp"]; exists && !toBool(udp) {
			parts = append(parts, "udp-relay=false")
		} else {
			addBool("udp-relay", true)
		}
		if p.Str("plugin") == "obfs" {
			o := p.Sub("plugin-opts")
			add("obfs", fmt.Sprint(o["mode"]))
			if h := fmt.Sprint(o["host"]); h != "" && h != "<nil>" {
				add("obfs-host", h)
			}
		}
	case "vmess":
		parts = append(parts, "vmess", host, port)
		add("username", p.Str("uuid"))
		addBool("tls", p.Bool("tls"))
		add("sni", p.Str("servername"))
		addBool("skip-cert-verify", p.Bool("skip-cert-verify"))
		if p.Str("network") == "ws" && !addSurgeWS(p, add) {
			return ""
		}
		addBool("vmess-aead", p.Int("alterId") == 0)
	case "vless":
		// Surge has no vless policy type; emitting it makes the whole
		// detached [Proxy] include fail to load.
		return ""
	case "trojan":
		parts = append(parts, "trojan", host, port)
		add("password", p.Str("password"))
		add("sni", p.Str("sni"))
		addBool("skip-cert-verify", p.Bool("skip-cert-verify"))
		// The official Trojan policy documents WS, not VLESS's grpc options.
		if p.Str("network") == "ws" && !addSurgeWS(p, add) {
			return ""
		}
	case "hysteria2":
		parts = append(parts, "hysteria2", host, port)
		add("password", p.Str("password"))
		add("sni", p.Str("sni"))
		addBool("skip-cert-verify", p.Bool("skip-cert-verify"))
		if d := p.Str("down"); d != "" {
			add("download-bandwidth", strings.TrimSuffix(strings.TrimSpace(d), " Mbps"))
		}
		if via == "" {
			add("port-hopping", p.Str("ports"))
		}
	case "tuic":
		parts = append(parts, "tuic-v5", host, port)
		add("uuid", p.Str("uuid"))
		add("password", p.Str("password"))
		add("sni", p.Str("sni"))
		if alpn := p.StrList("alpn"); len(alpn) > 0 {
			add("alpn", alpn[0])
		}
		addBool("skip-cert-verify", p.Bool("skip-cert-verify"))
	case "anytls":
		// Match the working Surge form:
		// Name = anytls, host, port, password=..., underlying-proxy="Front", sni=host
		parts = append(parts, "anytls", host, port)
		add("password", p.Str("password"))
		if via != "" {
			parts = append(parts, "underlying-proxy="+surgeQuote(via))
		}
		sni := p.Str("sni")
		if sni == "" {
			sni = host
		}
		add("sni", sni)
		addBool("skip-cert-verify", p.Bool("skip-cert-verify"))
		if via != "" {
			add("test-timeout", "8")
		}
	case "snell":
		parts = append(parts, "snell", host, port)
		add("psk", p.Str("psk"))
		add("version", strconv.Itoa(orInt(p.Int("version"), 4)))
		if oo := p.Sub("obfs-opts"); oo != nil {
			add("obfs", fmt.Sprint(oo["mode"]))
			if h := fmt.Sprint(oo["host"]); h != "" && h != "<nil>" {
				add("obfs-host", h)
			}
		}
		addBool("reuse", p.Bool("reuse"))
	case "socks5":
		if p.Bool("tls") {
			parts = append(parts, "socks5-tls", host, port)
		} else {
			parts = append(parts, "socks5", host, port)
		}
		if u := p.Str("username"); u != "" {
			parts = append(parts, u, p.Str("password"))
		}
	case "http":
		if p.Bool("tls") {
			parts = append(parts, "https", host, port)
		} else {
			parts = append(parts, "http", host, port)
		}
		if u := p.Str("username"); u != "" {
			parts = append(parts, u, p.Str("password"))
		}
	default:
		return ""
	}
	if p.Type == "trojan" {
		add("alpn", strings.Join(p.StrList("alpn"), ","))
	}
	if via != "" && p.Type != "anytls" {
		parts = append(parts, "underlying-proxy="+surgeQuote(via))
		add("test-timeout", "8")
	}
	if p.Type != "socks5" && p.Type != "http" {
		if p.Bool("tfo") {
			addBool("tfo", true)
		}
	}
	return surgeIdent(p.Name) + " = " + strings.Join(parts, ", ")
}

func orInt(n, d int) int {
	if n == 0 {
		return d
	}
	return n
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// SurgeGroupLine renders one [Proxy Group] line.
func SurgeGroupLine(g domain.ProxyGroup) string {
	typ := g.Type
	switch typ {
	case "", "select":
		typ = "select"
	case "load-balance":
		typ = "load-balance"
	case "relay":
		typ = "select" // Surge has no relay group; underlying-proxy is used instead
	}
	parts := []string{typ}
	for _, name := range g.Proxies {
		parts = append(parts, surgeIdent(name))
	}
	if typ == "url-test" || typ == "fallback" || typ == "load-balance" {
		url := g.URL
		if url == "" {
			url = "http://www.gstatic.com/generate_204"
		}
		parts = append(parts, "url="+url)
		if g.Interval > 0 {
			parts = append(parts, "interval="+strconv.Itoa(g.Interval))
		}
		if g.Tolerance > 0 {
			parts = append(parts, "tolerance="+strconv.Itoa(g.Tolerance))
		}
	}
	if g.Hidden {
		parts = append(parts, "hidden=true")
	}
	return g.Name + " = " + strings.Join(parts, ", ")
}

// surgeRule converts a mihomo rule to Surge syntax; "" drops it.
func surgeRule(r string) string {
	fields := strings.Split(r, ",")
	if len(fields) < 2 {
		return ""
	}
	typ := strings.ToUpper(strings.TrimSpace(fields[0]))
	switch typ {
	case "MATCH":
		return "FINAL," + strings.TrimSpace(fields[1])
	case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6", "GEOIP", "USER-AGENT", "URL-REGEX", "PROCESS-NAME", "DST-PORT", "SRC-IP", "IN-PORT", "RULE-SET", "DOMAIN-SET", "AND", "OR", "NOT", "PROTOCOL", "SCRIPT", "CELLULAR-RADIO", "DEVICE-NAME", "SUBNET", "IP-ASN":
		return strings.Join(fields, ",")
	case "GEOSITE":
		return "" // Surge has no GEOSITE; templates should use RULE-SET
	case "SRC-PORT":
		fields[0] = "SRC-PORT"
		return strings.Join(fields, ",")
	}
	return ""
}

// RenderSurge produces a Surge profile.
func RenderSurge(b *Bundle) (*Rendered, error) { return RenderSurgeWithProfile(b, "") }

func RenderSurgeWithProfile(b *Bundle, profile string) (*Rendered, error) {
	if !oneOf(profile, "", ProfileSurgeVLESS) {
		return nil, fmt.Errorf("未知 Surge 兼容档")
	}
	tpl := builtinSurgeTemplate
	if profile == ProfileSurgeVLESS {
		tpl = surgeFullTemplate
	}
	if b.Template != nil && b.Template.Kind == "surge" && strings.TrimSpace(b.Template.Content) != "" {
		tpl = b.Template.Content
	}
	if profile == ProfileSurgeVLESS && (tpl == builtinSurgeTemplate || tpl == surgeFullTemplate) {
		tpl = surgeFullTemplate
		for _, rule := range b.Rules {
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(rule)), "MATCH,") {
				tpl = strings.Replace(tpl, "\nFINAL,VpsCT Nodes\n", "\n", 1)
				break
			}
		}
	}
	var proxyLines, groupLines, ruleLines []string
	byName := map[string]string{}
	b, omitted := projectNodes(b, func(p proxynode.Proxy, via string) bool {
		var line string
		if profile == ProfileSurgeVLESS && p.Type == "vless" {
			line = modernVLESSLine(p, via)
		} else {
			line = SurgeProxyLine(p, via)
		}
		if line == "" {
			return false
		}
		byName[p.Name] = line
		return true
	})
	allNames := b.AllProxyNames()
	if len(allNames) == 0 && !b.ValidationOnly && (len(omitted) > 0 || tpl == builtinSurgeTemplate || strings.Contains(tpl, "{{all")) {
		return nil, noRenderPath(FormatSurge)
	}
	for _, name := range allNames {
		proxyLines = append(proxyLines, byName[name])
	}

	wantGroups := strings.Contains(tpl, "{{PROXY_GROUPS}}") || strings.Contains(tpl, "[Proxy Group]")
	// Only {{RULES}} opts into subscription rules. A stub [Rule]/FINAL is
	// required for remote #!include validation and must stay empty.
	wantRules := strings.Contains(tpl, "{{RULES}}")
	if wantGroups {
		groups := b.Groups
		if len(groups) == 0 && strings.Contains(tpl, "{{PROXY_GROUPS}}") {
			groups = []domain.ProxyGroup{{Name: "PROXY", Type: "select", Proxies: b.AllProxyNames()}}
		}
		for _, g := range groups {
			if g.Type == "relay" {
				return nil, fmt.Errorf("%w：Surge 无法将 relay 转为选择组", ErrNoRenderPath)
			}
			groupLines = append(groupLines, SurgeGroupLine(g))
		}
	}
	// Subscription MATCH/FINAL would short-circuit template RULE-SETs that
	// sit after {{RULES}}. Drop them when the skeleton already has a FINAL.
	if wantRules {
		tplHasFinal := strings.Contains(strings.ToUpper(strings.ReplaceAll(tpl, "{{RULES}}", "")), "FINAL,")
		for _, r := range b.Rules {
			if l := surgeRule(r); l != "" {
				if tplHasFinal && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(l)), "FINAL,") {
					continue
				}
				ruleLines = append(ruleLines, l)
			}
		}
	}
	out := expandAllTokens(strings.ReplaceAll(tpl, "{{NAME}}", surgeHeaderName(b.Name)), allNames)
	rep := func(marker string, lines []string, section string) {
		joined := strings.Join(lines, "\n")
		if strings.Contains(out, marker) {
			out = strings.ReplaceAll(out, marker, joined)
			return
		}
		if len(lines) == 0 {
			return
		}
		idx := strings.Index(out, "["+section+"]")
		if idx < 0 {
			out = strings.TrimRight(out, "\n") + "\n\n[" + section + "]\n" + joined + "\n"
			return
		}
		lineEnd := strings.Index(out[idx:], "\n")
		if lineEnd < 0 {
			out += "\n" + joined + "\n"
			return
		}
		pos := idx + lineEnd + 1
		out = out[:pos] + joined + "\n" + out[pos:]
	}
	if !strings.Contains(out, "{{PROXY_GROUPS}}") && len(groupLines) > 0 {
		out = mergeTextGroups(out, groupLines)
	} else {
		rep("{{PROXY_GROUPS}}", groupLines, "Proxy Group")
	}
	if len(ruleLines) > 0 || strings.Contains(out, "{{RULES}}") {
		rep("{{RULES}}", ruleLines, "Rule")
	}
	rep("{{PROXIES}}", proxyLines, "Proxy")
	if b.Userinfo != nil && len(b.InfoNodes) > 0 && !strings.Contains(out, "#!MANAGED-CONFIG") {
		info := "# " + strings.Join(b.InfoNodes, " | ") + "\n"
		if i := strings.Index(out, "[Proxy]"); i >= 0 {
			lineEnd := strings.Index(out[i:], "\n")
			if lineEnd < 0 {
				out += "\n" + info
			} else {
				pos := i + lineEnd + 1
				out = out[:pos] + info + out[pos:]
			}
		} else {
			out = info + out
		}
	}
	if !b.ValidationOnly {
		var err error
		out, err = checkTextPolicies(out, omitted, allNames)
		if err != nil {
			return nil, err
		}
	}
	return &Rendered{EmittedNames: allNames, Body: []byte(out), ContentType: "text/plain; charset=utf-8", Filename: b.Name + ".conf", Format: FormatSurge}, nil
}

func surgeHeaderName(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return "VpsCT"
	}
	return s
}
