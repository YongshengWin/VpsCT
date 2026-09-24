package proxynode

import (
	"regexp"
	"strings"
	"unicode"
)

// IsSubscriptionInfo recognizes complete metadata labels used by upstream
// subscriptions as dummy Shadowsocks proxies. It intentionally does not match
// real node names that merely contain quota/expiry words.
func IsSubscriptionInfo(p Proxy) bool {
	if p.Type != "ss" {
		return false
	}
	name := strings.TrimLeftFunc(strings.TrimSpace(p.Name), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsSymbol(r) || r == '\ufe0f' })
	return subscriptionInfoLabel.MatchString(name)
}

var subscriptionInfoLabel = regexp.MustCompile(`(?i)^(?:(?:剩余流量|剩餘流量|剩余用量|流量剩余|remaining\s+(?:traffic|data))\s*[:：]?\s*\d+(?:\.\d+)?\s*(?:[KMGTPE]i?B|bytes?)(?:\s*/\s*\d+(?:\.\d+)?\s*(?:[KMGTPE]i?B|bytes?))?|(?:过期时间|到期时间|到期日期|有效期至|過期時間|到期時間|expiry|expires?)\s*[:：]?\s*(?:\d{4}[-/.]\d{1,2}[-/.]\d{1,2}(?:[ T]\d{1,2}:\d{2}(?::\d{2})?)?|长期有效|長期有效|永久))$`)
