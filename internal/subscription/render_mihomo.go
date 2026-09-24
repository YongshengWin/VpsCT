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
	Body        []byte
	ContentType string
	Filename    string
	Format      string
}

var allMarker = regexp.MustCompile(`^\{\{\s*all(?:\|(.*?))?\s*\}\}$|^__ALL(?:_PROXIES)?__$`)

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
	tpl := builtinMihomoTemplate
	if b.Template != nil && b.Template.Kind == "mihomo" && strings.TrimSpace(b.Template.Content) != "" {
		tpl = b.Template.Content
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(tpl), &doc); err != nil {
		return nil, fmt.Errorf("模板 YAML 解析失败: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
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
	if len(b.Groups) > 0 {
		groupsNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, g := range b.Groups {
			groupsNode.Content = append(groupsNode.Content, groupMapNode(g))
		}
		setMapKey(root, "proxy-groups", groupsNode)
	} else if gn := getMapKey(root, "proxy-groups"); gn != nil && gn.Kind == yaml.SequenceNode {
		expandGroupMarkers(gn, allNames)
	} else {
		groupsNode := &yaml.Node{Kind: yaml.SequenceNode}
		def := domain.ProxyGroup{Name: "PROXY", Type: "select", Proxies: append([]string{}, allNames...)}
		if len(def.Proxies) == 0 {
			def.Proxies = []string{"DIRECT"}
		}
		groupsNode.Content = append(groupsNode.Content, groupMapNode(def))
		setMapKey(root, "proxy-groups", groupsNode)
	}
	if len(b.Rules) > 0 {
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
	// Templates may contain explicit references as well as {{all}} markers.
	if groups := getMapKey(root, "proxy-groups"); groups != nil {
		for _, group := range groups.Content {
			if members := getMapKey(group, "proxies"); members != nil && members.Kind == yaml.SequenceNode {
				kept := members.Content[:0]
				for _, member := range members.Content {
					if !omitted[member.Value] {
						kept = append(kept, member)
					}
				}
				if len(kept) == 0 {
					kept = []*yaml.Node{{Kind: yaml.ScalarNode, Value: "DIRECT"}}
				}
				members.Content = kept
			}
		}
	}
	if rules := getMapKey(root, "rules"); rules != nil && rules.Kind == yaml.SequenceNode {
		kept := rules.Content[:0]
		for _, rule := range rules.Content {
			parts := strings.Split(rule.Value, ",")
			i := len(parts) - 1
			if i > 0 && strings.TrimSpace(parts[i]) == "no-resolve" {
				i--
			}
			if !omitted[strings.TrimSpace(parts[i])] {
				kept = append(kept, rule)
			}
		}
		rules.Content = kept
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	enc.Close()
	return &Rendered{Body: buf.Bytes(), ContentType: "text/yaml; charset=utf-8", Filename: b.Name + ".yaml", Format: FormatMihomo}, nil
}

// Filter only the rendered copy: the node library and other formats keep their
// original versions. Version zero/omitted uses mihomo's default.
func mihomoCompatibleBundle(b *Bundle) (*Bundle, map[string]bool) {
	omitted := map[string]bool{}
	all := append([]proxynode.Proxy{}, b.Proxies...)
	via := map[string]string{}
	for _, c := range b.Chains {
		all = append(all, c.Proxy)
		via[c.Proxy.Name] = c.Via
	}
	for _, p := range all {
		if p.Type == "snell" && (p.Int("version") < 0 || p.Int("version") > 5) {
			omitted[p.Name] = true
		}
	}
	// Remove dependent chains too, regardless of their ordering.
	for changed := true; changed; {
		changed = false
		for _, p := range all {
			if !omitted[p.Name] && (omitted[via[p.Name]] || omitted[p.Str("dialer-proxy")]) {
				omitted[p.Name] = true
				changed = true
			}
		}
	}
	out := *b
	out.Proxies = nil
	out.Chains = nil
	for _, p := range b.Proxies {
		if !omitted[p.Name] {
			out.Proxies = append(out.Proxies, p)
		}
	}
	for _, c := range b.Chains {
		if !omitted[c.Proxy.Name] {
			out.Chains = append(out.Chains, c)
		}
	}
	return &out, omitted
}

func expandGroupMarkers(groups *yaml.Node, allNames []string) {
	for _, g := range groups.Content {
		if g.Kind != yaml.MappingNode {
			continue
		}
		list := getMapKey(g, "proxies")
		if list == nil || list.Kind != yaml.SequenceNode {
			continue
		}
		var expanded []*yaml.Node
		hasMarker := false
		for _, item := range list.Content {
			if item.Kind != yaml.ScalarNode {
				expanded = append(expanded, item)
				continue
			}
			m := allMarker.FindStringSubmatch(strings.TrimSpace(item.Value))
			if m == nil {
				expanded = append(expanded, item)
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
				expanded = append(expanded, &yaml.Node{Kind: yaml.ScalarNode, Value: n})
			}
		}
		if len(expanded) == 0 {
			if hasMarker && len(allNames) > 0 {
				for _, name := range allNames {
					expanded = append(expanded, &yaml.Node{Kind: yaml.ScalarNode, Value: name})
				}
			} else {
				expanded = []*yaml.Node{{Kind: yaml.ScalarNode, Value: "DIRECT"}}
			}
		}
		list.Content = expanded
	}
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
