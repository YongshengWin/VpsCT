package subscription

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/sshconfig"
	"ctlvps/internal/wgconfig"
)

// SingBoxOutbound converts a proxy into a sing-box client outbound. ok=false
// when the protocol has no sing-box outbound (e.g. snell).
func SingBoxOutbound(p proxynode.Proxy, via string) (map[string]any, bool) {
	o := map[string]any{"tag": p.Name, "server": p.Server, "server_port": p.Port}
	tls := func(serverNameKey string, force bool) map[string]any {
		if !force && !p.Bool("tls") {
			return nil
		}
		t := map[string]any{"enabled": true}
		if sn := p.Str(serverNameKey); sn != "" {
			t["server_name"] = sn
		}
		if p.Bool("skip-cert-verify") {
			t["insecure"] = true
		}
		if alpn := p.StrList("alpn"); len(alpn) > 0 {
			t["alpn"] = alpn
		}
		if fp := p.Str("client-fingerprint"); fp != "" {
			t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		if ro := p.Sub("reality-opts"); ro != nil {
			r := map[string]any{"enabled": true, "public_key": ro["public-key"]}
			if sid, ok := ro["short-id"]; ok && fmt.Sprint(sid) != "" {
				r["short_id"] = fmt.Sprint(sid)
			}
			t["reality"] = r
			if _, ok := t["utls"]; !ok {
				t["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
			}
		}
		return t
	}
	transport := func() map[string]any {
		switch p.Str("network") {
		case "ws", "httpupgrade":
			tr := map[string]any{"type": p.Str("network")}
			if wo := p.Sub("ws-opts"); wo != nil {
				if path := fmt.Sprint(wo["path"]); path != "" && path != "<nil>" {
					tr["path"] = path
				}
				if h, ok := wo["headers"].(map[string]any); ok {
					if host := fmt.Sprint(h["Host"]); host != "" && host != "<nil>" {
						if tr["type"] == "ws" {
							tr["headers"] = map[string]any{"Host": host}
						} else {
							tr["host"] = host
						}
					}
				}
				if ed, ok := wo["max-early-data"]; ok {
					tr["max_early_data"] = ed
					tr["early_data_header_name"] = "Sec-WebSocket-Protocol"
				}
			}
			return tr
		case "grpc":
			tr := map[string]any{"type": "grpc"}
			if go_ := p.Sub("grpc-opts"); go_ != nil {
				tr["service_name"] = go_["grpc-service-name"]
			}
			return tr
		case "h2", "http":
			tr := map[string]any{"type": "http"}
			key := "h2-opts"
			if p.Str("network") == "http" {
				key = "http-opts"
			}
			if ho := p.Sub(key); ho != nil {
				if path, ok := ho["path"]; ok {
					if l, ok := path.([]any); ok && len(l) > 0 {
						tr["path"] = l[0]
					} else {
						tr["path"] = path
					}
				}
				if host, ok := ho["host"]; ok {
					tr["host"] = host
				}
			}
			return tr
		}
		return nil
	}
	switch p.Type {
	case "ss":
		o["type"] = "shadowsocks"
		o["method"] = p.Str("cipher")
		o["password"] = p.Str("password")
		if plugin := p.Str("plugin"); plugin != "" {
			po := p.Sub("plugin-opts")
			switch plugin {
			case "obfs":
				o["plugin"] = "obfs-local"
				opts := "obfs=" + fmt.Sprint(po["mode"])
				if h := fmt.Sprint(po["host"]); h != "" && h != "<nil>" {
					opts += ";obfs-host=" + h
				}
				o["plugin_opts"] = opts
			case "v2ray-plugin":
				o["plugin"] = "v2ray-plugin"
				var parts []string
				if toBool(po["tls"]) {
					parts = append(parts, "tls")
				}
				if h := fmt.Sprint(po["host"]); h != "" && h != "<nil>" {
					parts = append(parts, "host="+h)
				}
				if pa := fmt.Sprint(po["path"]); pa != "" && pa != "<nil>" {
					parts = append(parts, "path="+pa)
				}
				o["plugin_opts"] = strings.Join(parts, ";")
			}
		}
		if p.Bool("udp-over-tcp") {
			o["udp_over_tcp"] = true
		}
	case "vmess":
		o["type"] = "vmess"
		o["uuid"] = p.Str("uuid")
		o["security"] = orDefault(p.Str("cipher"), "auto")
		o["alter_id"] = p.Int("alterId")
		if t := tls("servername", false); t != nil {
			o["tls"] = t
		}
		if tr := transport(); tr != nil {
			o["transport"] = tr
		}
	case "vless":
		o["type"] = "vless"
		o["uuid"] = p.Str("uuid")
		if f := p.Str("flow"); f != "" {
			o["flow"] = f
		}
		if t := tls("servername", false); t != nil {
			o["tls"] = t
		}
		if tr := transport(); tr != nil {
			o["transport"] = tr
		}
		if pe := p.Str("packet-encoding"); pe != "" {
			o["packet_encoding"] = pe
		}
	case "trojan":
		o["type"] = "trojan"
		o["password"] = p.Str("password")
		o["tls"] = tls("sni", true)
		if tr := transport(); tr != nil {
			o["transport"] = tr
		}
	case "hysteria2":
		o["type"] = "hysteria2"
		o["password"] = p.Str("password")
		t := tls("sni", true)
		if _, ok := t["alpn"]; !ok {
			t["alpn"] = []string{"h3"}
		}
		o["tls"] = t
		if obfs := p.Str("obfs"); obfs != "" {
			o["obfs"] = map[string]any{"type": obfs, "password": p.Str("obfs-password")}
		}
		if up := mbps(p.Str("up")); up > 0 {
			o["up_mbps"] = up
		}
		if down := mbps(p.Str("down")); down > 0 {
			o["down_mbps"] = down
		}
		if ports := p.Str("ports"); ports != "" {
			o["server_ports"] = strings.Split(strings.ReplaceAll(ports, "-", ":"), ",")
		}
	case "tuic":
		o["type"] = "tuic"
		o["uuid"] = p.Str("uuid")
		o["password"] = p.Str("password")
		o["congestion_control"] = orDefault(p.Str("congestion-controller"), "bbr")
		o["udp_relay_mode"] = orDefault(p.Str("udp-relay-mode"), "native")
		if p.Bool("reduce-rtt") {
			o["zero_rtt_handshake"] = true
		}
		t := tls("sni", true)
		if _, ok := t["alpn"]; !ok {
			t["alpn"] = []string{"h3"}
		}
		o["tls"] = t
	case "anytls":
		o["type"] = "anytls"
		o["password"] = p.Str("password")
		o["tls"] = tls("sni", true)
	case "ssh":
		config, err := sshconfig.Decode(p.Params)
		if err != nil {
			return nil, false
		}
		for key, value := range config.SingBox() {
			o[key] = value
		}
	case "socks5":
		o["type"] = "socks"
		o["version"] = "5"
		if u := p.Str("username"); u != "" {
			o["username"] = u
			o["password"] = p.Str("password")
		}
	case "http":
		o["type"] = "http"
		if u := p.Str("username"); u != "" {
			o["username"] = u
			o["password"] = p.Str("password")
		}
		if t := tls("sni", false); t != nil {
			o["tls"] = t
		}
	default:
		return nil, false
	}
	if via != "" {
		o["detour"] = via
	}
	return o, true
}

func mbps(s string) int {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSuffix(s, "mbps")
	s = strings.TrimSpace(s)
	n, _ := strconv.Atoi(s)
	return n
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	}
	return false
}

// autoRuleSet builds a remote rule_set declaration for a GEOSITE/GEOIP tag
// (SagerNet's pre-built .srs files) so rules referencing sets the template
// did not declare are kept instead of being dropped.
func autoRuleSet(tag string) map[string]any {
	repo := "sing-geosite"
	if strings.HasPrefix(tag, "geoip-") {
		repo = "sing-geoip"
	}
	return map[string]any{
		"tag":             tag,
		"type":            "remote",
		"format":          "binary",
		"url":             fmt.Sprintf("https://raw.githubusercontent.com/SagerNet/%s/rule-set/%s.srs", repo, tag),
		"download_detour": "direct",
	}
}

// singboxRules converts mihomo rules into sing-box route rules; returns the
// rules, the final outbound (from MATCH) if present, and any rule_set
// declarations that had to be synthesised for GEOSITE/GEOIP references.
func singboxRules(rules []string, knownTags map[string]bool, ruleSets map[string]bool) ([]map[string]any, string, []map[string]any) {
	var out []map[string]any
	var added []map[string]any
	final := ""
	ensureSet := func(tag string) {
		if ruleSets[tag] {
			return
		}
		ruleSets[tag] = true
		added = append(added, autoRuleSet(tag))
	}
	var cur map[string]any
	curTarget := ""
	flush := func() {
		if cur != nil {
			out = append(out, cur)
			cur = nil
		}
	}
	appendList := func(key, val string) {
		if l, ok := cur[key].([]string); ok {
			cur[key] = append(l, val)
		} else {
			cur[key] = []string{val}
		}
	}
	mapTarget := func(t string) string {
		switch t {
		case "DIRECT":
			return "direct"
		case "REJECT", "REJECT-DROP":
			return "reject"
		}
		return t
	}
	for _, r := range rules {
		f := strings.Split(r, ",")
		if len(f) < 2 {
			continue
		}
		typ := strings.ToUpper(strings.TrimSpace(f[0]))
		if typ == "MATCH" {
			final = mapTarget(strings.TrimSpace(f[1]))
			continue
		}
		if len(f) < 3 {
			continue
		}
		val := strings.TrimSpace(f[1])
		target := mapTarget(strings.TrimSpace(f[2]))
		if target != "reject" && target != "direct" && !knownTags[target] {
			continue
		}
		key := ""
		switch typ {
		case "DOMAIN":
			key = "domain"
		case "DOMAIN-SUFFIX":
			key = "domain_suffix"
		case "DOMAIN-KEYWORD":
			key = "domain_keyword"
		case "DOMAIN-REGEX":
			key = "domain_regex"
		case "IP-CIDR", "IP-CIDR6":
			key = "ip_cidr"
		case "DST-PORT":
			key = "port"
		case "PROCESS-NAME":
			key = "process_name"
		case "GEOIP":
			key = "rule_set"
			val = "geoip-" + strings.ToLower(val)
			ensureSet(val)
		case "GEOSITE":
			key = "rule_set"
			val = "geosite-" + strings.ToLower(val)
			ensureSet(val)
		case "RULE-SET":
			key = "rule_set"
			if !ruleSets[val] {
				continue
			}
		default:
			continue
		}
		if cur == nil || curTarget != target {
			flush()
			cur = map[string]any{}
			curTarget = target
			if target == "reject" {
				cur["action"] = "reject"
			} else {
				cur["outbound"] = target
			}
		}
		if key == "port" {
			n, _ := strconv.Atoi(val)
			if l, ok := cur["port"].([]int); ok {
				cur["port"] = append(l, n)
			} else {
				cur["port"] = []int{n}
			}
			continue
		}
		appendList(key, val)
	}
	flush()
	return out, final, added
}

// SingBoxEndpoint renders the 1.11+ endpoint shape, never the removed outbound.
// Remote-DNS and TCP-only mihomo semantics have no equivalent endpoint option.
func SingBoxEndpoint(p proxynode.Proxy, via string) (map[string]any, bool) {
	if p.Type != "wireguard" {
		return nil, false
	}
	c, err := wgconfig.Decode(p.Params)
	if err != nil || c.RemoteDNS || !c.UDP {
		return nil, false
	}
	ep := c.Endpoint(p.Server, p.Port)
	ep["tag"] = p.Name
	if via != "" {
		ep["detour"] = via
	}
	return ep, true
}

// RenderSingBox produces a sing-box client JSON config.
func RenderSingBox(b *Bundle) (*Rendered, error) {
	tpl := builtinSingBoxTemplate
	if b.Template != nil && b.Template.Kind == "singbox" && strings.TrimSpace(b.Template.Content) != "" {
		tpl = b.Template.Content
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(tpl), &doc); err != nil {
		return nil, fmt.Errorf("sing-box 模板 JSON 解析失败: %w", err)
	}
	var nodeOutbounds []map[string]any
	var nodeEndpoints []map[string]any
	knownTags := map[string]bool{"direct": true}
	var nodeTags []string
	for _, p := range b.Proxies {
		if ep, ok := SingBoxEndpoint(p, ""); ok {
			nodeEndpoints = append(nodeEndpoints, ep)
			nodeTags = append(nodeTags, p.Name)
			knownTags[p.Name] = true
			continue
		}
		if o, ok := SingBoxOutbound(p, ""); ok {
			nodeOutbounds = append(nodeOutbounds, o)
			nodeTags = append(nodeTags, p.Name)
			knownTags[p.Name] = true
		}
	}
	for _, c := range b.Chains {
		if ep, ok := SingBoxEndpoint(c.Proxy, c.Via); ok && knownTags[c.Via] {
			nodeEndpoints = append(nodeEndpoints, ep)
			nodeTags = append(nodeTags, c.Proxy.Name)
			knownTags[c.Proxy.Name] = true
			continue
		}
		if o, ok := SingBoxOutbound(c.Proxy, c.Via); ok && knownTags[c.Via] {
			nodeOutbounds = append(nodeOutbounds, o)
			nodeTags = append(nodeTags, c.Proxy.Name)
			knownTags[c.Proxy.Name] = true
		}
	}
	groups := b.Groups
	if len(groups) == 0 {
		if extracted := extractTemplateGroups(doc, nodeTags); len(extracted) > 0 {
			groups = extracted
		} else {
			groups = []domain.ProxyGroup{{Name: "PROXY", Type: "select", Proxies: nodeTags}}
		}
	}
	for _, g := range groups {
		knownTags[g.Name] = true
	}
	var groupOutbounds []map[string]any
	for _, g := range groups {
		var members []string
		for _, m := range g.Proxies {
			switch m {
			case "DIRECT":
				members = append(members, "direct")
			case "REJECT", "REJECT-DROP", "PASS":
				continue
			default:
				if knownTags[m] {
					members = append(members, m)
				}
			}
		}
		if len(members) == 0 {
			members = []string{"direct"}
		}
		o := map[string]any{"tag": g.Name, "outbounds": members}
		switch g.Type {
		case "url-test", "fallback", "load-balance":
			o["type"] = "urltest"
			o["url"] = orDefault(g.URL, "https://www.gstatic.com/generate_204")
			o["interval"] = fmt.Sprintf("%ds", orInt(g.Interval, 300))
			if g.Tolerance > 0 {
				o["tolerance"] = g.Tolerance
			}
		default:
			o["type"] = "selector"
			o["default"] = members[0]
		}
		groupOutbounds = append(groupOutbounds, o)
	}
	// keep template system outbounds (direct/dns/block...)
	var sys []any
	if list, ok := doc["outbounds"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				t := fmt.Sprint(m["type"])
				if t == "direct" || t == "block" || t == "dns" {
					sys = append(sys, m)
				}
			}
		}
	}
	if len(sys) == 0 {
		sys = []any{map[string]any{"type": "direct", "tag": "direct"}}
	}
	outbounds := make([]any, 0, len(groupOutbounds)+len(nodeOutbounds)+len(sys))
	for _, g := range groupOutbounds {
		outbounds = append(outbounds, g)
	}
	for _, n := range nodeOutbounds {
		outbounds = append(outbounds, n)
	}
	outbounds = append(outbounds, sys...)
	doc["outbounds"] = outbounds
	if len(nodeEndpoints) > 0 {
		existing, _ := doc["endpoints"].([]any)
		var merged []any
		for _, raw := range existing {
			ep, ok := raw.(map[string]any)
			if ok && !knownTags[fmt.Sprint(ep["tag"])] {
				merged = append(merged, ep)
			}
		}
		for _, ep := range nodeEndpoints {
			merged = append(merged, ep)
		}
		doc["endpoints"] = merged
	}

	route, _ := doc["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
	}
	ruleSets := map[string]bool{}
	if rs, ok := route["rule_set"].([]any); ok {
		for _, item := range rs {
			if m, ok := item.(map[string]any); ok {
				ruleSets[fmt.Sprint(m["tag"])] = true
			}
		}
	}
	converted, final, addedSets := singboxRules(b.Rules, knownTags, ruleSets)
	if len(addedSets) > 0 {
		list, _ := route["rule_set"].([]any)
		for _, rs := range addedSets {
			list = append(list, rs)
		}
		route["rule_set"] = list
	}
	fallback := "direct"
	if len(groups) > 0 {
		fallback = groups[0].Name
	}
	rewriteDNSDetours(doc, knownTags, fallback)

	var existing []any
	if list, ok := route["rules"].([]any); ok {
		existing = filterKnownOutbounds(list, knownTags)
	}
	for _, r := range converted {
		existing = append(existing, r)
	}
	route["rules"] = existing
	if final != "" {
		route["final"] = final
	} else if _, ok := route["final"]; !ok && len(groups) > 0 {
		route["final"] = groups[0].Name
	}
	doc["route"] = route
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return &Rendered{Body: body, ContentType: "application/json; charset=utf-8", Filename: b.Name + ".json", Format: FormatSingBox}, nil
}

// extractTemplateGroups turns selector/urltest outbounds in the skeleton into
// groups and expands {{all}} / {{all|regex}} against generated node tags.
func extractTemplateGroups(doc map[string]any, allNames []string) []domain.ProxyGroup {
	list, ok := doc["outbounds"].([]any)
	if !ok {
		return nil
	}
	var out []domain.ProxyGroup
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t := fmt.Sprint(m["type"])
		if t != "selector" && t != "urltest" {
			continue
		}
		g := domain.ProxyGroup{Name: fmt.Sprint(m["tag"]), Type: "select"}
		if t == "urltest" {
			g.Type = "url-test"
			if u, ok := m["url"].(string); ok {
				g.URL = u
			}
		}
		var names []string
		if obs, ok := m["outbounds"].([]any); ok {
			for _, o := range obs {
				names = append(names, fmt.Sprint(o))
			}
		}
		g.Proxies = expandAllMarkers(names, allNames)
		out = append(out, g)
	}
	return out
}

func rewriteDNSDetours(doc map[string]any, knownTags map[string]bool, fallback string) {
	dns, ok := doc["dns"].(map[string]any)
	if !ok {
		return
	}
	servers, _ := dns["servers"].([]any)
	for _, s := range servers {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		d, _ := m["detour"].(string)
		if d == "" || d == "direct" || knownTags[d] {
			continue
		}
		m["detour"] = fallback
	}
}

func filterKnownOutbounds(rules []any, knownTags map[string]bool) []any {
	kept := make([]any, 0, len(rules))
	for _, item := range rules {
		m, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		ob, _ := m["outbound"].(string)
		if ob != "" && ob != "direct" && ob != "reject" && !knownTags[ob] {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}
