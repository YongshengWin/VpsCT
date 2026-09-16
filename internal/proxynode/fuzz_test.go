package proxynode

import "testing"

// Malformed URI/YAML/JSON must remain bounded and must not panic.
func FuzzParseAny(f *testing.F) {
	for _, s := range []string{"proxies: []", "ss://bad", "{", "proxies: [&a [*a]]", "vmess://AAAA"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 65536 {
			return
		}
		_ = ParseAny(s)
	})
}
