package subscription

import (
	"regexp"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
)

var regionNames = []struct {
	code string
	re   *regexp.Regexp
}{
	{"HK", regexp.MustCompile(`(?i)(香港|hong[ -]?kong|🇭🇰|(?:^|[^a-z])hk(?:[^a-z]|$))`)},
	{"SG", regexp.MustCompile(`(?i)(新加坡|singapore|🇸🇬|(?:^|[^a-z])sg(?:[^a-z]|$))`)},
	{"JP", regexp.MustCompile(`(?i)(日本|japan|🇯🇵|(?:^|[^a-z])jp(?:[^a-z]|$))`)},
	{"US", regexp.MustCompile(`(?i)(美国|united[ -]?states|🇺🇸|(?:^|[^a-z])(?:us|usa)(?:[^a-z]|$))`)},
	{"NL", regexp.MustCompile(`(?i)(荷兰|netherlands|🇳🇱|(?:^|[^a-z])nl(?:[^a-z]|$))`)},
}

// inferRegion avoids guessing when a name contains multiple region signals.
func inferRegion(name string) (string, string) {
	region := ""
	for _, rule := range regionNames {
		if rule.re.MatchString(name) {
			if region != "" && region != rule.code {
				return "", "ambiguous"
			}
			region = rule.code
		}
	}
	if region != "" {
		return region, "name"
	}
	return "", "unknown"
}

func setProxyRegion(p *proxynode.Proxy, n domain.Node, servers map[int64]string) {
	if code := strings.ToUpper(strings.TrimSpace(n.ExitRegion)); len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' {
		p.Region, p.RegionSource = code, "manual"
		return
	}
	// A configured egress may lead to a different location than the ingress
	// server. Without verified egress metadata, do not claim its country.
	if n.Network != nil && n.Network.EgressProfileID != 0 {
		p.Region, p.RegionSource = "", "egress_unknown"
		return
	}
	if n.ServerID != nil {
		if code := strings.ToUpper(strings.TrimSpace(servers[*n.ServerID])); len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' {
			p.Region, p.RegionSource = code, "server"
			return
		}
	}
	p.Region, p.RegionSource = inferRegion(n.Name)
}

func proxyRegion(p proxynode.Proxy) string {
	if p.Region != "" || p.RegionSource != "" {
		return p.Region
	}
	code, _ := inferRegion(p.Name)
	return code
}
