package subscription

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
)

// Rendered is a client-ready subscription payload.
type Rendered struct {
	// EmittedNames excludes policy names and status comments. Not part of the client payload.
	EmittedNames []string `json:"-"`
	Body         []byte
	ContentType  string
	Filename     string
	Format       string
}

var allMarker = regexp.MustCompile(`^\{\{\s*all(?:\|(.*?))?\s*\}\}$|^__ALL(?:_PROXIES)?__$`)
var regionMarker = regexp.MustCompile(`^\{\{\s*region:([A-Z]{2}|UNKNOWN)\s*\}\}$`)

// expandAllMarkers replaces {{all}} / {{all|regex}} tokens with generated node names.
func expandAllMarkers(items, allNames []string) []string {
	var out []string
	hasMarker := false
	for _, item := range items {
		m := allMarker.FindStringSubmatch(strings.TrimSpace(item))
		if m == nil {
			out = append(out, item)
			continue
		}
		hasMarker = true
		var re *regexp.Regexp
		if len(m) > 1 && m[1] != "" {
			re, _ = regexp.Compile(m[1])
		}
		for _, n := range allNames {
			if re != nil && !re.MatchString(n) {
				continue
			}
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		// A missing region must not silently bypass usable subscription nodes.
		if hasMarker && len(allNames) > 0 {
			return append([]string(nil), allNames...)
		}
		return []string{"DIRECT"}
	}
	return out
}

// RenderMihomo produces a Clash/mihomo YAML config.
func RenderMihomo(b *Bundle) (*Rendered, error) {
	b, omitted := mihomoCompatibleBundle(b)
	if len(omitted) > 0 && len(b.Proxies)+len(b.Chains) == 0 {
		return nil, noRenderPath(FormatMihomo)
	}
	tpl := builtinMihomoTemplate
	if b.Template != nil && b.Template.Kind == "mihomo" && strings.TrimSpace(b.Template.Content) != "" {
		tpl = b.Template.Content
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(tpl), &doc); err != nil {
		return nil, fmt.Errorf("模板 YAML 解析失败: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("Mihomo 模板必须为 YAML 映射")
	}
	root := doc.Content[0]

	// Do not inject fake "剩余流量" SS nodes: FlClash/mihomo already read
	// Subscription-Userinfo, and a 127.0.0.1 first proxy makes refresh fail.
	proxies := append([]proxynode.Proxy{}, b.Proxies...)
	proxyNodes := &yaml.Node{Kind: yaml.SequenceNode}
	for _, p := range proxies {
		proxyNodes.Content = append(proxyNodes.Content, proxyMapNode(p.ClashMap()))
	}
	for _, c := range b.Chains {
		m := c.Proxy.ClashMap()
		m["dialer-proxy"] = c.Via
		proxyNodes.Content = append(proxyNodes.Content, proxyMapNode(m))
	}
	setMapKey(root, "proxies", proxyNodes)

	allNames := b.AllProxyNames()
	omittedGroups := map[string]bool{}
	if len(b.Groups) > 0 {
		groupsNode := &yaml.Node{Kind: yaml.SequenceNode}
		// A groups-only override must keep template policies used by the
		// untouched rules. Override matching names, rather than deleting them.
		if prior := getMapKey(root, "proxy-groups"); len(b.Rules) == 0 && prior != nil && prior.Kind == yaml.SequenceNode {
			names := map[string]bool{}
			for _, g := range b.Groups {
				names[g.Name] = true
			}
			for _, g := range prior.Content {
				if n := getMapKey(g, "name"); n != nil && !names[n.Value] {
					groupsNode.Content = append(groupsNode.Content, g)
				}
			}
			other, err := buildOtherRegionGroups(root, groupsNode, b)
			if err != nil {
				return nil, err
			}
			omittedGroups, err = expandMihomoGroupMarkers(groupsNode, b, other)
			if err != nil {
				return nil, err
			}
		}
		for _, g := range b.Groups {
			groupsNode.Content = append(groupsNode.Content, groupMapNode(g))
		}
		setMapKey(root, "proxy-groups", groupsNode)
	} else if gn := getMapKey(root, "proxy-groups"); gn != nil && gn.Kind == yaml.SequenceNode {
		var err error
		otherRegions, err := buildOtherRegionGroups(root, gn, b)
		if err != nil {
			return nil, err
		}
		omittedGroups, err = expandMihomoGroupMarkers(gn, b, otherRegions)
		if err != nil {
			return nil, err
		}
		if rules := getMapKey(root, "rules"); rules != nil && rules.Kind == yaml.SequenceNode && len(b.Rules) == 0 {
			for _, rule := range rules.Content {
				parts := strings.Split(rule.Value, ",")
				i := len(parts) - 1
				if i > 0 && strings.EqualFold(strings.TrimSpace(parts[i]), "no-resolve") {
					i--
				}
				if i >= 0 && omittedGroups[strings.TrimSpace(parts[i])] {
					return nil, fmt.Errorf("%w：规则引用没有匹配节点的分组 %q，请选择其他策略", ErrNoRenderPath, strings.TrimSpace(parts[i]))
				}
			}
		}
	} else {
		groupsNode := &yaml.Node{Kind: yaml.SequenceNode}
		def := domain.ProxyGroup{Name: "PROXY", Type: "select", Proxies: append([]string{}, allNames...)}
		if len(def.Proxies) == 0 {
			if !b.ValidationOnly {
				return nil, noRenderPath(FormatMihomo)
			}
			def.Proxies = []string{"REJECT"}
		}
		groupsNode.Content = append(groupsNode.Content, groupMapNode(def))
		setMapKey(root, "proxy-groups", groupsNode)
	}
	deleteMapKey(root, "x-vpsct-other-regions")
	if len(b.Rules) > 0 {
		for _, rule := range b.Rules {
			parts := strings.Split(rule, ",")
			i := len(parts) - 1
			if i > 0 && strings.EqualFold(strings.TrimSpace(parts[i]), "no-resolve") {
				i--
			}
			if i >= 0 && omittedGroups[strings.TrimSpace(parts[i])] {
				return nil, fmt.Errorf("%w：规则引用没有匹配节点的分组 %q，请选择其他策略", ErrNoRenderPath, strings.TrimSpace(parts[i]))
			}
		}
		rulesNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, r := range b.Rules {
			rulesNode.Content = append(rulesNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: r})
		}
		setMapKey(root, "rules", rulesNode)
		if !rulesUseRuleSet(b.Rules) {
			deleteMapKey(root, "rule-providers")
		}
	}
	if len(b.RuleProviders) > 0 {
		var rp yaml.Node
		if err := rp.Encode(b.RuleProviders); err == nil {
			setMapKey(root, "rule-providers", &rp)
		}
	}
	// Validate the actual emitted policies, not just the names in the input.
	known := map[string]bool{}
	for _, name := range allNames {
		known[name] = true
	}
	if groups := getMapKey(root, "proxy-groups"); groups != nil {
		for _, group := range groups.Content {
			if n := getMapKey(group, "name"); n != nil {
				known[n.Value] = true
			}
		}
		for _, group := range groups.Content {
			members := getMapKey(group, "proxies")
			if members == nil || members.Kind != yaml.SequenceNode {
				if !b.ValidationOnly && !dynamicMihomoGroup(group) {
					return nil, noRenderPath(FormatMihomo)
				}
				continue
			}
			kept := members.Content[:0]
			for _, member := range members.Content {
				if omitted[member.Value] {
					if t := getMapKey(group, "type"); t != nil && t.Value == "relay" {
						return nil, fmt.Errorf("%w：Mihomo relay 缺少必要成员", ErrNoRenderPath)
					}
					continue
				}
				if !b.ValidationOnly && !builtinPolicies[member.Value] && !known[member.Value] {
					return nil, fmt.Errorf("%w：Mihomo 代理组引用不可用的策略 %q", ErrNoRenderPath, member.Value)
				}
				kept = append(kept, member)
			}
			if len(kept) == 0 && !dynamicMihomoGroup(group) && !b.ValidationOnly {
				return nil, fmt.Errorf("%w：Mihomo 代理组没有可用成员", ErrNoRenderPath)
			}
			members.Content = kept
		}
	}
	if rules := getMapKey(root, "rules"); rules != nil && !b.ValidationOnly {
		for _, r := range rules.Content {
			if target := rulePolicy(r.Value); target != "" && !known[target] && !builtinPolicies[target] {
				return nil, fmt.Errorf("%w：Mihomo 规则引用不可用的策略 %q", ErrNoRenderPath, target)
			}
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	enc.Close()
	return &Rendered{EmittedNames: allNames, Body: buf.Bytes(), ContentType: "text/yaml; charset=utf-8", Filename: b.Name + ".yaml", Format: FormatMihomo}, nil
}

// Filter only the rendered copy: the node library and other formats keep their
// original versions. Version zero/omitted uses mihomo's default.
func mihomoCompatibleBundle(b *Bundle) (*Bundle, map[string]bool) {
	return projectNodes(b, func(p proxynode.Proxy, _ string) bool {
		if ambiguousSimpleTLS(p) || surgeOnlyFingerprint(p) || p.Str("network") == "xhttp" && p.Type != "vless" {
			return false
		}
		switch p.Type {
		case "socks5":
			// Mihomo always sends the server hostname as SNI; name-cert-verify only changes certificate verification.
			sni := nonempty(p.Str("sni"), p.Str("servername"))
			return (sni == "" || strings.EqualFold(sni, p.Server)) && !p.Bool("remote-dns-resolve") && p.Params["headers"] == nil
		case "http":
			return p.Str("servername") == "" || p.Str("servername") == p.Str("sni")
		case "hysteria2": // Certificate pin replaces PKI rather than supplementing it.
			return p.Str("fingerprint") == "" || p.Bool("skip-cert-verify")
		case "snell":
			return p.Int("version") >= 0 && p.Int("version") <= 5
		}
		return true
	})
}

// Built-in templates may opt into groups for additional explicitly known
// regions. Generate them from subscription nodes, not from a global list.
func buildOtherRegionGroups(root, groups *yaml.Node, b *Bundle) ([]string, error) {
	setting := getMapKey(root, "x-vpsct-other-regions")
	if setting == nil || setting.Value != "true" {
		return nil, nil
	}
	declared, names := map[string]bool{}, map[string]bool{}
	for _, group := range groups.Content {
		if name := getMapKey(group, "name"); name != nil {
			names[name.Value] = true
		}
		if members := getMapKey(group, "proxies"); members != nil {
			for _, member := range members.Content {
				if m := regionMarker.FindStringSubmatch(strings.TrimSpace(member.Value)); m != nil {
					declared[m[1]] = true
				}
			}
		}
	}
	codes := map[string]bool{}
	for _, p := range b.Proxies {
		if code := proxyRegion(p); code != "" && !declared[code] {
			codes[code] = true
		}
	}
	for _, c := range b.Chains {
		if code := proxyRegion(c.Proxy); code != "" && !declared[code] {
			codes[code] = true
		}
	}
	ordered := make([]string, 0, len(codes))
	for code := range codes {
		ordered = append(ordered, code)
	}
	sort.Strings(ordered)
	result := make([]string, 0, len(ordered))
	for _, code := range ordered {
		name := "🌍 " + code
		if names[name] {
			return nil, fmt.Errorf("自动地区组名称 %q 已被占用", name)
		}
		g := domain.ProxyGroup{Name: name, Type: "url-test", URL: "http://cp.cloudflare.com/generate_204", Interval: 300, Proxies: []string{"{{region:" + code + "}}"}}
		groups.Content = append(groups.Content, groupMapNode(g))
		result = append(result, name)
	}
	return result, nil
}

func expandMihomoGroupMarkers(groups *yaml.Node, b *Bundle, otherRegions []string) (map[string]bool, error) {
	allNames := b.AllProxyNames()
	regions := map[string]string{}
	for _, p := range b.Proxies {
		regions[p.Name] = proxyRegion(p)
	}
	for _, c := range b.Chains {
		regions[c.Proxy.Name] = proxyRegion(c.Proxy)
	}
	removed := map[string]bool{}
	keptGroups := make([]*yaml.Node, 0, len(groups.Content))
	for _, g := range groups.Content {
		if g.Kind != yaml.MappingNode {
			keptGroups = append(keptGroups, g)
			continue
		}
		list := getMapKey(g, "proxies")
		if list == nil || list.Kind != yaml.SequenceNode {
			keptGroups = append(keptGroups, g)
			continue
		}
		var expanded []*yaml.Node
		hasMarker, filtered := false, false
		for _, item := range list.Content {
			if item.Kind != yaml.ScalarNode {
				expanded = append(expanded, item)
				continue
			}
			value := strings.TrimSpace(item.Value)
			if value == "{{other-regions}}" {
				hasMarker, filtered = true, true
				for _, name := range otherRegions {
					expanded = append(expanded, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
				}
				continue
			}
			if region := regionMarker.FindStringSubmatch(value); region != nil {
				hasMarker, filtered = true, true
				for _, n := range allNames {
					if regions[n] == region[1] || region[1] == "UNKNOWN" && regions[n] == "" {
						expanded = append(expanded, &yaml.Node{Kind: yaml.ScalarNode, Value: n})
					}
				}
				continue
			}
			m := allMarker.FindStringSubmatch(value)
			if m == nil {
				expanded = append(expanded, item)
				continue
			}
			hasMarker = true
			var re *regexp.Regexp
			if len(m) > 1 && m[1] != "" {
				filtered = true
				var err error
				re, err = regexp.Compile(m[1])
				if err != nil {
					return nil, fmt.Errorf("代理组节点筛选正则无效: %w", err)
				}
			}
			for _, n := range allNames {
				if re != nil && !re.MatchString(n) {
					continue
				}
				expanded = append(expanded, &yaml.Node{Kind: yaml.ScalarNode, Value: n})
			}
		}
		if len(expanded) == 0 {
			if filtered {
				if b.ValidationOnly && len(allNames) == 0 {
					// Syntax-only checks cannot know which regions the future
					// subscription will contain. Use a non-routable placeholder
					// solely for this disposable render.
					list.Content = []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: "REJECT"}}
					keptGroups = append(keptGroups, g)
					continue
				}
				if name := getMapKey(g, "name"); name != nil {
					removed[name.Value] = true
				}
				continue
			}
			// Only the disposable syntax-check render may synthesize a member.
			if hasMarker {
				if !b.ValidationOnly {
					return nil, noRenderPath(FormatMihomo)
				}
				expanded = []*yaml.Node{{Kind: yaml.ScalarNode, Value: "REJECT"}}
			}
		}
		list.Content = expanded
		keptGroups = append(keptGroups, g)
	}
	groups.Content = keptGroups
	for _, group := range keptGroups {
		members := getMapKey(group, "proxies")
		if members == nil || members.Kind != yaml.SequenceNode {
			continue
		}
		kept := members.Content[:0]
		for _, member := range members.Content {
			if !removed[member.Value] {
				kept = append(kept, member)
			}
		}
		if len(kept) == 0 && len(members.Content) > 0 {
			return nil, fmt.Errorf("%w：代理组依赖的地区组没有匹配节点，请配置明确的备用策略", ErrNoRenderPath)
		}
		members.Content = kept
	}
	return removed, nil
}

// Provider-backed groups need not enumerate static members.
func dynamicMihomoGroup(group *yaml.Node) bool {
	if use := getMapKey(group, "use"); use != nil && len(use.Content) > 0 {
		return true
	}
	for _, key := range []string{"include-all", "include-all-proxies", "include-all-providers"} {
		if node := getMapKey(group, key); node != nil && node.Value == "true" {
			return true
		}
	}
	return false
}

var proxyKeyOrder = []string{"name", "type", "server", "port", "ports"}

func proxyMapNode(m map[string]any) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	emitted := map[string]bool{}
	add := func(k string) {
		v, ok := m[k]
		if !ok || emitted[k] {
			return
		}
		emitted[k] = true
		var vn yaml.Node
		if err := vn.Encode(v); err != nil {
			return
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &vn)
	}
	for _, k := range proxyKeyOrder {
		add(k)
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !emitted[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		add(k)
	}
	return n
}

func groupMapNode(g domain.ProxyGroup) *yaml.Node {
	m := map[string]any{"name": g.Name, "type": g.Type, "proxies": g.Proxies}
	if g.URL != "" {
		m["url"] = g.URL
	}
	if g.Interval > 0 {
		m["interval"] = g.Interval
	}
	if g.Tolerance > 0 {
		m["tolerance"] = g.Tolerance
	}
	if g.Lazy {
		m["lazy"] = true
	}
	if g.Icon != "" {
		m["icon"] = g.Icon
	}
	if g.Hidden {
		m["hidden"] = true
	}
	if g.Strategy != "" {
		m["strategy"] = g.Strategy
	}
	if g.DisableUDP {
		m["disable-udp"] = true
	}
	if g.InterfaceName != "" {
		m["interface-name"] = g.InterfaceName
	}
	if g.RoutingMark > 0 {
		m["routing-mark"] = g.RoutingMark
	}
	if g.ExpectedStatus != "" {
		m["expected-status"] = g.ExpectedStatus
	}
	n := &yaml.Node{Kind: yaml.MappingNode}
	order := []string{"name", "type", "url", "interval", "tolerance", "lazy", "strategy", "proxies"}
	emitted := map[string]bool{}
	add := func(k string) {
		v, ok := m[k]
		if !ok || emitted[k] {
			return
		}
		emitted[k] = true
		var vn yaml.Node
		if err := vn.Encode(v); err != nil {
			return
		}
		if k == "proxies" {
			vn.Style = yaml.FlowStyle
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &vn)
	}
	for _, k := range order {
		add(k)
	}
	rest := []string{}
	for k := range m {
		if !emitted[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		add(k)
	}
	return n
}

func getMapKey(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func rulesUseRuleSet(rules []string) bool {
	for _, r := range rules {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(r)), "RULE-SET,") {
			return true
		}
	}
	return false
}

func deleteMapKey(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func setMapKey(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, val)
}
