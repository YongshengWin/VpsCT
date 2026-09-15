package subscription

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
)

// Formats.
const (
	FormatMihomo       = "mihomo"
	FormatClash        = "clash" // alias of mihomo
	FormatRaw          = "raw"   // base64 share links
	FormatURIList      = "uri"   // plain share links
	FormatSurge        = "surge"
	FormatShadowrocket = "shadowrocket"
	FormatSingBox      = "singbox"
)

// KnownFormats lists user-selectable output formats.
var KnownFormats = []string{FormatMihomo, FormatRaw, FormatURIList, FormatSurge, FormatShadowrocket, FormatSingBox}

// NormalizeFormat maps aliases to canonical names ("" -> "").
func NormalizeFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case FormatClash, FormatMihomo, "clash.meta", "meta":
		return FormatMihomo
	case FormatRaw, "base64", "v2ray", "v2rayn", "v2rayng", "v2rayu":
		return FormatRaw
	case FormatURIList, "uri-list", "links", "plain":
		return FormatURIList
	case FormatSurge:
		return FormatSurge
	case FormatShadowrocket, "rocket":
		return FormatShadowrocket
	case FormatSingBox, "sing-box", "sfa", "sfi":
		return FormatSingBox
	}
	return ""
}

// DetectFormat guesses the client format from its User-Agent.
func DetectFormat(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "shadowrocket"):
		return FormatShadowrocket
	case strings.Contains(l, "surge"):
		return FormatSurge
	case strings.Contains(l, "sing-box"), strings.Contains(l, "sfa"), strings.Contains(l, "sfi"), strings.Contains(l, "sfm"), strings.Contains(l, "hiddify"), strings.Contains(l, "karing"):
		return FormatSingBox
	case strings.Contains(l, "clash"), strings.Contains(l, "mihomo"), strings.Contains(l, "stash"), strings.Contains(l, "verge"), strings.Contains(l, "flclash"), strings.Contains(l, "nyanpasu"):
		return FormatMihomo
	case strings.Contains(l, "v2rayn"), strings.Contains(l, "v2rayng"), strings.Contains(l, "v2rayu"), strings.Contains(l, "v2rayx"), strings.Contains(l, "nekobox"), strings.Contains(l, "nekoray"), strings.Contains(l, "quantumult"), strings.Contains(l, "loon"):
		return FormatRaw
	case strings.Contains(l, "v2ray"):
		return FormatRaw
	}
	return ""
}

// ChainedProxy is a proxy that must be dialed through another one.
type ChainedProxy struct {
	Proxy proxynode.Proxy
	Via   string
}

// Bundle is the format-independent result of the generator.
type Bundle struct {
	Name          string
	Proxies       []proxynode.Proxy
	Chains        []ChainedProxy
	Groups        []domain.ProxyGroup
	Rules         []string
	RuleProviders map[string]any
	Template      *domain.RuleTemplate
	Userinfo      *Userinfo
	InfoNodes     []string
	GeneratedAt   time.Time
}

// AllProxyNames returns proxies + chains in output order.
func (b *Bundle) AllProxyNames() []string {
	out := make([]string, 0, len(b.Proxies)+len(b.Chains))
	for _, p := range b.Proxies {
		out = append(out, p.Name)
	}
	for _, c := range b.Chains {
		out = append(out, c.Proxy.Name)
	}
	return out
}

// builtinPolicies are names that never need to exist as proxies.
var builtinPolicies = map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true, "GLOBAL": true, "no-resolve": true}

// ResolveGroups expands node ids / filters into concrete proxy names and drops
// dangling references so that clients never see an empty or broken group.
func ResolveGroups(groups []domain.ProxyGroup, proxies []proxynode.Proxy, chains []ChainedProxy, nameByID map[int64]string) []domain.ProxyGroup {
	allNames := make([]string, 0, len(proxies)+len(chains))
	for _, p := range proxies {
		allNames = append(allNames, p.Name)
	}
	for _, c := range chains {
		allNames = append(allNames, c.Proxy.Name)
	}
	groupNames := map[string]bool{}
	for _, g := range groups {
		groupNames[g.Name] = true
	}
	proxySet := map[string]bool{}
	for _, n := range allNames {
		proxySet[n] = true
	}
	out := make([]domain.ProxyGroup, 0, len(groups))
	for _, g := range groups {
		var members []string
		seen := map[string]bool{}
		add := func(n string) {
			if n == "" || seen[n] || n == g.Name {
				return
			}
			seen[n] = true
			members = append(members, n)
		}
		for _, p := range g.Proxies {
			if builtinPolicies[p] || groupNames[p] || proxySet[p] {
				add(p)
			}
		}
		for _, id := range g.NodeIDs {
			if n, ok := nameByID[id]; ok && proxySet[n] {
				add(n)
			}
		}
		if g.IncludeAll || g.Filter != "" {
			var re, exre *regexp.Regexp
			if g.Filter != "" {
				re, _ = regexp.Compile(g.Filter)
			}
			if g.ExcludeFilter != "" {
				exre, _ = regexp.Compile(g.ExcludeFilter)
			}
			for _, n := range allNames {
				if re != nil && !re.MatchString(n) {
					continue
				}
				if exre != nil && exre.MatchString(n) {
					continue
				}
				add(n)
			}
		}
		if len(members) == 0 {
			// avoid invalid config: fall back to DIRECT
			members = []string{"DIRECT"}
		}
		g2 := g
		g2.Proxies = members
		g2.NodeIDs = nil
		g2.Filter = ""
		g2.ExcludeFilter = ""
		g2.IncludeAll = false
		if g2.Type == "" {
			g2.Type = "select"
		}
		if (g2.Type == "url-test" || g2.Type == "fallback" || g2.Type == "load-balance") && g2.URL == "" {
			g2.URL = "https://www.gstatic.com/generate_204"
		}
		if (g2.Type == "url-test" || g2.Type == "fallback" || g2.Type == "load-balance") && g2.Interval == 0 {
			g2.Interval = 300
		}
		out = append(out, g2)
	}
	return out
}

// InfoProxies turns info lines into harmless pseudo nodes.
func InfoProxies(lines []string) []proxynode.Proxy {
	out := make([]proxynode.Proxy, 0, len(lines))
	for i, l := range lines {
		out = append(out, proxynode.Proxy{Name: l, Type: "ss", Server: "127.0.0.1", Port: 1080 + i, Params: map[string]any{"cipher": "aes-128-gcm", "password": "info", "udp": false}})
	}
	return out
}

// ValidateGroups checks group references for the editor (returns human errors).
func ValidateGroups(groups []domain.ProxyGroup) []string {
	var errs []string
	names := map[string]bool{}
	for _, g := range groups {
		if strings.TrimSpace(g.Name) == "" {
			errs = append(errs, "存在未命名的代理组")
			continue
		}
		if names[g.Name] {
			errs = append(errs, fmt.Sprintf("代理组名称重复: %s", g.Name))
		}
		names[g.Name] = true
		switch g.Type {
		case "", "select", "url-test", "fallback", "load-balance", "relay", "smart":
		default:
			errs = append(errs, fmt.Sprintf("代理组 %s 类型不支持: %s", g.Name, g.Type))
		}
		if g.Filter != "" {
			if _, err := regexp.Compile(g.Filter); err != nil {
				errs = append(errs, fmt.Sprintf("代理组 %s 过滤正则无效: %v", g.Name, err))
			}
		}
	}
	// cycle detection on group -> group references
	adj := map[string][]string{}
	for _, g := range groups {
		for _, p := range g.Proxies {
			if names[p] {
				adj[g.Name] = append(adj[g.Name], p)
			}
		}
	}
	state := map[string]int{}
	var visit func(n string) bool
	visit = func(n string) bool {
		if state[n] == 1 {
			return true
		}
		if state[n] == 2 {
			return false
		}
		state[n] = 1
		for _, m := range adj[n] {
			if visit(m) {
				return true
			}
		}
		state[n] = 2
		return false
	}
	for n := range names {
		if visit(n) {
			errs = append(errs, fmt.Sprintf("代理组存在循环引用（涉及 %s）", n))
			break
		}
	}
	return errs
}
