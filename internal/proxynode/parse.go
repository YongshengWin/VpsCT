package proxynode

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ParseResult is the outcome of parsing arbitrary subscription/node text.
type ParseResult struct {
	Proxies []Proxy
	Errors  []string
	// ClashDoc is the full decoded document when the input was Clash YAML.
	ClashDoc map[string]any
	Format   string // "clash" | "uri-list" | "base64"
}

// ParseAny auto-detects Clash YAML, a base64 blob, share links, or Surge
// proxy lines and returns the proxies found.
func ParseAny(text string) ParseResult {
	if len(text) > 16<<20 {
		return ParseResult{Errors: []string{"订阅内容过大"}}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ParseResult{Errors: []string{"empty input"}}
	}
	if strings.Contains(trimmed, "[Interface]") {
		return parseWireGuard(trimmed)
	}
	if looksLikeClash(trimmed) {
		res := parseClashYAML(trimmed)
		if len(res.Proxies) > 0 || res.ClashDoc != nil {
			res.Format = "clash"
			return res
		}
	}
	if !strings.Contains(trimmed, "://") {
		if dec, err := decodeBlob(trimmed); err == nil && strings.Contains(dec, "://") {
			res := parseURIList(dec)
			res.Format = "base64"
			return res
		}
	}
	res := parseURIList(trimmed)
	res.Format = "uri-list"
	return res
}

func decodeBlob(s string) (string, error) {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", errors.New("not base64")
}

func looksLikeClash(s string) bool {
	return strings.Contains(s, "proxies:") || strings.Contains(s, "proxy-groups:") || strings.HasPrefix(s, "port:") || strings.HasPrefix(s, "mixed-port:")
}

func parseURIList(text string) ParseResult {
	var res ParseResult
	for i, line := range strings.Split(text, "\n") {
		if i >= 10000 {
			res.Errors = append(res.Errors, "订阅条目过多")
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		p, err := ParseURI(line)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("第 %d 行不是有效的节点", i+1))
			continue
		}
		if err := validateProxy(p); err != nil {
			res.Errors = append(res.Errors, "节点参数无效")
			continue
		}
		res.Proxies = append(res.Proxies, p)
	}
	return res
}

func parseClashYAML(text string) ParseResult {
	var res ParseResult
	var doc map[string]any
	if err := boundedYAML(text, &doc); err != nil {
		res.Errors = append(res.Errors, "yaml: "+err.Error())
		return res
	}
	res.ClashDoc = normalizeYAML(doc).(map[string]any)
	list, _ := res.ClashDoc["proxies"].([]any)
	for i, item := range list {
		if i >= 10000 {
			res.Errors = append(res.Errors, "订阅条目过多")
			break
		}
		m, ok := item.(map[string]any)
		if !ok {
			res.Errors = append(res.Errors, fmt.Sprintf("proxies[%d]: not a map", i))
			continue
		}
		p, err := FromClashMap(m)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		if err := validateProxy(p); err != nil {
			res.Errors = append(res.Errors, "节点参数无效")
			continue
		}
		res.Proxies = append(res.Proxies, p)
	}
	return res
}

// DedupeNames makes proxy names unique by appending " 2", " 3", ...
func DedupeNames(list []Proxy) []Proxy {
	count := map[string]int{}
	used := map[string]bool{}
	for i := range list {
		name := list[i].Name
		if name == "" {
			name = list[i].HostPort()
		}
		candidate := name
		for used[candidate] {
			count[name]++
			candidate = fmt.Sprintf("%s %d", name, count[name]+1)
		}
		used[candidate] = true
		list[i].Name = candidate
	}
	return list
}
