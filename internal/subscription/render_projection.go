package subscription

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"ctlvps/internal/proxynode"
)

// Project the dependency graph before expanding selectors. A missing, cyclic or
// incompatible front invalidates all its dependents, regardless of input order.
// Explicit chains and imported dialer-proxy entries have the same semantics.
func projectNodes(b *Bundle, supported func(proxynode.Proxy, string) bool) (*Bundle, map[string]bool) {
	type entry struct {
		p       proxynode.Proxy
		via     string
		invalid bool
	}
	entries := make(map[string]entry, len(b.Proxies)+len(b.Chains))
	names := b.AllProxyNames()
	add := func(p proxynode.Proxy, via string, chain bool) {
		param := p.Str("dialer-proxy")
		_, duplicate := entries[p.Name]
		invalid := duplicate || p.Name == "" || chain && (via == "" || param != "" && param != via)
		if !chain {
			via = param
		}
		entries[p.Name] = entry{p, via, invalid}
	}
	for _, p := range b.Proxies {
		add(p, "", false)
	}
	for _, c := range b.Chains {
		add(c.Proxy, c.Via, true)
	}
	state := map[string]int{}
	var keep func(string) bool
	keep = func(name string) bool {
		if state[name] != 0 {
			return state[name] == 2
		}
		e, ok := entries[name]
		if !ok || e.invalid {
			return false
		}
		state[name] = 1
		if (e.via == "" || keep(e.via)) && supported(e.p, e.via) {
			state[name] = 2
			return true
		}
		return false
	}
	out := *b
	out.Proxies, out.Chains = nil, nil
	omitted := map[string]bool{}
	for _, name := range names {
		if !keep(name) {
			omitted[name] = true
			continue
		}
		e := entries[name]
		if e.via == "" {
			out.Proxies = append(out.Proxies, e.p)
		} else {
			out.Chains = append(out.Chains, ChainedProxy{Proxy: e.p, Via: e.via})
		}
	}
	return &out, omitted
}

var ErrNoRenderPath = errors.New("目标格式没有可用订阅路径")

func noRenderPath(format string) error {
	return fmt.Errorf("%w：%s 订阅没有可等价表示的节点或前置链路，请更换格式或节点", ErrNoRenderPath, format)
}

// The optional no-resolve flag is not a routing policy.
func rulePolicy(rule string) string {
	fields := strings.Split(rule, ",")
	if len(fields) < 2 {
		return ""
	}
	i := len(fields) - 1
	if strings.EqualFold(strings.TrimSpace(fields[i]), "no-resolve") {
		i--
	}
	return strings.TrimSpace(fields[i])
}

func ambiguousSimpleTLS(p proxynode.Proxy) bool {
	return oneOf(p.Type, "http", "socks5") && !p.Bool("tls") && (p.Str("sni") != "" || p.Str("servername") != "" || p.Bool("skip-cert-verify"))
}

func surgeOnlyFingerprint(p proxynode.Proxy) bool {
	return oneOf(p.Str("client-fingerprint"), "chromium-155", "apple-26")
}

func isTUICv5(p proxynode.Proxy) bool {
	return p.Str("token") == "" && (p.Str("version") == "" || p.Str("version") == "5") && p.Str("uuid") != ""
}

func oneOf(s string, choices ...string) bool { return slices.Contains(choices, s) }
