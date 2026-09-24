package subscription

import (
	"ctlvps/internal/proxynode"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestMissingRegionUsesAvailableProxies(t *testing.T) {
	p := proxynode.Proxy{Name: "US fixture", Type: "ss", Server: "example.test", Port: 443, Params: map[string]any{"cipher": "aes-128-gcm", "password": "synthetic"}}
	bundle := &Bundle{Name: "US-only share", Proxies: []proxynode.Proxy{p}}
	m, e := RenderMihomo(bundle)
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = yaml.Unmarshal(m.Body, &doc); e != nil {
		t.Fatal(e)
	}
	for _, raw := range doc["proxy-groups"].([]any) {
		g := raw.(map[string]any)
		if g["type"] == "url-test" {
			members := g["proxies"].([]any)
			if len(members) != 1 || members[0] != p.Name {
				t.Fatalf("empty region bypasses nodes: %v", g["name"])
			}
		}
	}
	if _, ok := doc["global-client-fingerprint"]; ok {
		t.Fatal("removed Mihomo field emitted")
	}
	s, e := RenderSingBox(bundle)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(s.Body, &doc); e != nil {
		t.Fatal(e)
	}
	for _, raw := range doc["outbounds"].([]any) {
		g := raw.(map[string]any)
		if g["type"] == "urltest" {
			members := g["outbounds"].([]any)
			if len(members) != 1 || members[0] != p.Name {
				t.Fatalf("empty sing-box region bypasses nodes: %v", g["tag"])
			}
		}
	}
	if got := expandAllTokens("region = url-test, {{all|JP}}", []string{p.Name}); got != "region = url-test, US fixture" {
		t.Fatalf("text client fallback: %s", got)
	}
	if got := expandAllMarkers([]string{"DIRECT"}, []string{p.Name}); len(got) != 1 || got[0] != "DIRECT" {
		t.Fatal("explicit direct policy changed")
	}
	if got := expandAllMarkers([]string{"{{all|JP}}"}, nil); len(got) != 1 || got[0] != "DIRECT" {
		t.Fatal("empty subscription fallback invalid")
	}
	if got := expandAllMarkers([]string{"{{all|JP}}"}, []string{"JP fixture", p.Name}); len(got) != 1 || got[0] != "JP fixture" {
		t.Fatal("working region selection widened")
	}
}
