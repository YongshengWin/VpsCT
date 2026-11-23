package subscription

import (
	"fmt"
	"strconv"
	"strings"

	"ctlvps/internal/proxynode"
)

// expandAllTokens replaces {{all}} / {{all|regex}} anywhere in s.
func expandAllTokens(s string, names []string) string {
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			b.WriteString(rest)
			break
		}
		j += i + 2
		tok := rest[i:j]
		if allMarker.FindStringSubmatch(tok) == nil {
			b.WriteString(rest[:j])
			rest = rest[j:]
			continue
		}
		b.WriteString(rest[:i])
		expanded := expandAllMarkers([]string{tok}, names)
		for i, n := range expanded {
			expanded[i] = surgeIdent(n)
		}
		b.WriteString(strings.Join(expanded, ","))
		rest = rest[j:]
	}
	return b.String()
}

func nonempty(v ...string) string {
	for _, s := range v {
		if s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}

func mapStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s := fmt.Sprint(m[k])
	if s == "<nil>" {
		return ""
	}
	return s
}

// ShadowrocketProxyLine renders one [Proxy] line. Unlike Surge, VLESS/Reality is supported.
func ShadowrocketProxyLine(p proxynode.Proxy, via string) string {
	// Native [Proxy] parameters are not share URI or Surge parameters. Do
	// not publish a bare transport, an unpinned peer or an unobfuscated Hy2.
	if surgeOnlyFingerprint(p) || p.Str("fingerprint") != "" || p.Type == "vless" && !oneOf(p.Str("network"), "", "tcp", "ws") || p.Type == "hysteria2" && (p.Str("obfs") != "" || p.Str("obfs-password") != "") {
		return ""
	}
	if p.Type != "vless" && p.Type != "snell" {
		return SurgeProxyLine(p, via)
	}
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
	if p.Type == "snell" {
		// Shadowrocket's native [Proxy] syntax uses password, not Surge's psk.
		parts = append(parts, "snell", p.Server, strconv.Itoa(p.Port))
		add("password", p.Str("psk"))
		add("version", strconv.Itoa(orInt(p.Int("version"), 4)))
		if oo := p.Sub("obfs-opts"); oo != nil {
			add("obfs", mapStr(oo, "mode"))
			add("obfs-host", mapStr(oo, "host"))
		}
		if p.Bool("udp") {
			add("udp", "1")
		}
		addBool("reuse", p.Bool("reuse"))
		addBool("tfo", p.Bool("tfo"))
		if via != "" {
			parts = append(parts, "underlying-proxy="+surgeQuote(via))
			add("test-timeout", "8")
		}
		return surgeIdent(p.Name) + " = " + strings.Join(parts, ", ")
	}
	parts = append(parts, "vless", p.Server, strconv.Itoa(p.Port))
	add("encrypt-method", nonempty(p.Str("encryption"), "none"))
	add("password", p.Str("uuid"))
	net := p.Str("network")
	if net == "" || net == "tcp" {
		add("obfs", "none")
	} else {
		add("obfs", net)
	}
	if net == "ws" {
		path, host, ok := p.BasicWebSocket()
		if !ok || strings.ContainsAny(path+host, "\r\n\x00\"") {
			return ""
		}
		add("obfs-uri", path)
		add("obfs-host", host)
	}
	addBool("udp-relay", true)
	add("peer", nonempty(p.Str("servername"), p.Str("sni")))
	if ro := p.Sub("reality-opts"); ro != nil {
		addBool("tls", true)
		addBool("reality", true)
		add("pbk", mapStr(ro, "public-key"))
		add("sid", mapStr(ro, "short-id"))
	} else if p.Bool("tls") {
		addBool("tls", true)
	}
	if p.Bool("skip-cert-verify") {
		add("allow-insecure", "true")
	}
	add("flow", p.Str("flow"))
	add("fp", p.Str("client-fingerprint"))
	if via != "" {
		parts = append(parts, "underlying-proxy="+surgeQuote(via))
	}
	return surgeIdent(p.Name) + " = " + strings.Join(parts, ", ")
}

// RenderShadowrocket produces a full Shadowrocket .conf (groups + rules + nodes).
func RenderShadowrocket(b *Bundle) (*Rendered, error) {
	tpl := builtinShadowrocketTemplate
	if b.Template != nil && b.Template.Kind == "shadowrocket" && strings.TrimSpace(b.Template.Content) != "" {
		tpl = b.Template.Content
	}
	if !strings.Contains(tpl, "{{PROXIES}}") && len(b.AllProxyNames()) > 0 {
		return nil, fmt.Errorf("Shadowrocket 模板缺少 {{PROXIES}}，无法写入订阅节点")
	}
	var proxyLines []string
	byName := map[string]string{}
	b, omitted := projectNodes(b, func(p proxynode.Proxy, via string) bool {
		line := ShadowrocketProxyLine(p, via)
		if line == "" {
			return false
		}
		byName[p.Name] = line
		return true
	})
	allNames := b.AllProxyNames()
	if len(allNames) == 0 && !b.ValidationOnly && (len(omitted) > 0 || tpl == builtinShadowrocketTemplate || strings.Contains(tpl, "{{all")) {
		return nil, noRenderPath(FormatShadowrocket)
	}
	for _, name := range allNames {
		proxyLines = append(proxyLines, byName[name])
	}

	var groupLines []string
	if len(b.Groups) > 0 {
		for _, g := range b.Groups {
			if g.Type == "relay" {
				return nil, fmt.Errorf("%w：Shadowrocket 无法将 relay 转为选择组", ErrNoRenderPath)
			}
			gg := g
			if len(g.Proxies) > 0 {
				gg.Proxies = expandAllMarkers(g.Proxies, allNames)
			}
			groupLines = append(groupLines, SurgeGroupLine(gg))
		}
	}
	var ruleLines []string
	if strings.Contains(tpl, "{{RULES}}") {
		tplHasFinal := strings.Contains(strings.ToUpper(tpl), "FINAL,")
		for _, r := range b.Rules {
			if l := surgeRule(r); l != "" {
				u := strings.ToUpper(strings.TrimSpace(l))
				if tplHasFinal && (strings.HasPrefix(u, "FINAL,") || strings.HasPrefix(u, "MATCH,")) {
					continue
				}
				ruleLines = append(ruleLines, l)
			}
		}
	}
	out := expandAllTokens(strings.ReplaceAll(tpl, "{{NAME}}", surgeHeaderName(b.Name)), allNames)
	if strings.Contains(out, "{{PROXY_GROUPS}}") {
		out = strings.ReplaceAll(out, "{{PROXY_GROUPS}}", strings.Join(groupLines, "\n"))
	} else if len(groupLines) > 0 {
		out = mergeTextGroups(out, groupLines)
	}
	out = strings.ReplaceAll(out, "{{RULES}}", strings.Join(ruleLines, "\n"))
	out = strings.ReplaceAll(out, "{{PROXIES}}", strings.Join(proxyLines, "\n"))
	if !b.ValidationOnly {
		var err error
		out, err = checkTextPolicies(out, omitted, allNames)
		if err != nil {
			return nil, err
		}
	}
	return &Rendered{EmittedNames: allNames, Body: []byte(out), ContentType: "text/plain; charset=utf-8", Filename: b.Name + ".conf", Format: FormatShadowrocket}, nil
}
