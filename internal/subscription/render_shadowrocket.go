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
		b.WriteString(strings.Join(expandAllMarkers([]string{tok}, names), ","))
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
		if o := p.Sub("ws-opts"); o != nil {
			add("obfs-uri", mapStr(o, "path"))
			if h, ok := o["headers"].(map[string]any); ok {
				add("obfs-host", mapStr(h, "Host"))
			}
		}
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
	var proxyLines []string
	emitted := map[string]bool{}
	for _, p := range b.Proxies {
		if l := ShadowrocketProxyLine(p, ""); l != "" {
			proxyLines = append(proxyLines, l)
			emitted[p.Name] = true
		}
	}
	for _, c := range b.Chains {
		if c.Via != "" && !emitted[c.Via] {
			continue
		}
		if l := ShadowrocketProxyLine(c.Proxy, c.Via); l != "" {
			proxyLines = append(proxyLines, l)
			emitted[c.Proxy.Name] = true
		}
	}
	allNames := b.AllProxyNames()
	var groupLines []string
	if len(b.Groups) > 0 {
		for _, g := range b.Groups {
			gg := g
			gg.Proxies = expandAllMarkers(g.Proxies, allNames)
			groupLines = append(groupLines, SurgeGroupLine(gg))
		}
	}
	var ruleLines []string
	if strings.Contains(tpl, "{{RULES}}") {
		for _, r := range b.Rules {
			if l := surgeRule(r); l != "" {
				u := strings.ToUpper(strings.TrimSpace(l))
				if strings.HasPrefix(u, "FINAL,") || strings.HasPrefix(u, "MATCH,") {
					continue
				}
				ruleLines = append(ruleLines, l)
			}
		}
	}
	out := tpl
	out = strings.ReplaceAll(out, "{{PROXIES}}", strings.Join(proxyLines, "\n"))
	if strings.Contains(out, "{{PROXY_GROUPS}}") {
		out = strings.ReplaceAll(out, "{{PROXY_GROUPS}}", strings.Join(groupLines, "\n"))
	} else if len(groupLines) > 0 {
		out = replaceSection(out, "Proxy Group", strings.Join(groupLines, "\n"))
	}
	out = strings.ReplaceAll(out, "{{RULES}}", strings.Join(ruleLines, "\n"))
	out = strings.ReplaceAll(out, "{{NAME}}", surgeHeaderName(b.Name))
	out = expandAllTokens(out, allNames)
	return &Rendered{Body: []byte(out), ContentType: "text/plain; charset=utf-8", Filename: b.Name + ".conf", Format: FormatShadowrocket}, nil
}

func replaceSection(src, section, body string) string {
	tag := "[" + section + "]"
	i := strings.Index(src, tag)
	if i < 0 {
		return strings.TrimRight(src, "\n") + "\n\n" + tag + "\n" + body + "\n"
	}
	start := i + len(tag)
	if start < len(src) && src[start] == '\n' {
		start++
	}
	end := len(src)
	for k := start; k < len(src); k++ {
		if src[k] == '[' && (k == 0 || src[k-1] == '\n') {
			end = k
			break
		}
	}
	return src[:start] + body + "\n" + src[end:]
}
