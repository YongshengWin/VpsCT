package subscription

import "ctlvps/internal/proxynode"

// StandaloneURI applies the same link constraints to subscriptions, the node
// library and access routes. A standalone link cannot describe a required front.
func StandaloneURI(p proxynode.Proxy) (string, error) {
	if oneOf(p.Type, "http", "socks5") && (p.Params["headers"] != nil || p.Bool("remote-dns-resolve") || p.Str("sni") != "" || p.Str("servername") != "" || p.Bool("skip-cert-verify") || p.Str("fingerprint") != "" || p.Str("client-fingerprint") != "" || len(p.StrList("alpn")) != 0) {
		return "", proxynode.ErrUnsupported
	}
	if p.Type == "socks5" && p.Bool("tls") || p.Type == "tuic" && !isTUICv5(p) {
		return "", proxynode.ErrUnsupported
	}
	return proxynode.ToURI(p)
}

// StandaloneMihomo returns the same proxy mapping used by a rendered profile.
func StandaloneMihomo(p proxynode.Proxy) map[string]any {
	b, _ := mihomoCompatibleBundle(&Bundle{Proxies: []proxynode.Proxy{p}})
	if len(b.Proxies) == 0 {
		return nil
	}
	return p.ClashMap()
}
