package subscription

import (
	"strings"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/proxynode"
	"gopkg.in/yaml.v3"
)

func TestRegionDetectionIsConservative(t *testing.T) {
	for _, tc := range []struct{ name, want, source string }{
		{"Japan Tokyo", "JP", "name"}, {"🇭🇰 HK 01", "HK", "name"},
		{"business cloud", "", "unknown"}, {"JP to US", "", "ambiguous"},
	} {
		got, source := inferRegion(tc.name)
		if got != tc.want || source != tc.source {
			t.Fatalf("%q got %q/%q", tc.name, got, source)
		}
	}
	id := int64(7)
	p := proxynode.Proxy{Name: "JP misleading"}
	setProxyRegion(&p, domain.Node{Name: p.Name, ServerID: &id}, map[int64]string{id: "HK"})
	if p.Region != "HK" || p.RegionSource != "server" {
		t.Fatalf("server region was not authoritative: %+v", p)
	}
	setProxyRegion(&p, domain.Node{Name: "Japan", ServerID: &id, Network: &networkconfig.Node{EgressProfileID: 5}}, map[int64]string{id: "HK"})
	if p.Region != "" || p.RegionSource != "egress_unknown" {
		t.Fatalf("unverified egress assigned server region: %+v", p)
	}
	setProxyRegion(&p, domain.Node{Name: "Japan", ServerID: &id, ExitRegion: "US", Network: &networkconfig.Node{EgressProfileID: 5}}, map[int64]string{id: "HK"})
	if p.Region != "US" || p.RegionSource != "manual" {
		t.Fatalf("manual exit region not preferred: %+v", p)
	}
}

func TestMihomoRegionMembershipAndInvalidFilter(t *testing.T) {
	valid := func(name, region string) proxynode.Proxy {
		return proxynode.Proxy{Name: name, Region: region, RegionSource: "server", Type: "ss", Server: "fixture.test", Port: 443, Params: map[string]any{"cipher": "aes-128-gcm", "password": "synthetic"}}
	}
	b := &Bundle{Name: "test", Proxies: []proxynode.Proxy{valid("JP misleading", "HK"), valid("unknown node", ""), valid("German node", "DE")}}
	r, err := RenderMihomo(b)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Groups []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(r.Body, &doc); err != nil {
		t.Fatal(err)
	}
	groups := map[string][]string{}
	for _, group := range doc.Groups {
		groups[group.Name] = group.Proxies
	}
	if len(groups["🇭🇰 香港"]) != 1 || groups["🇭🇰 香港"][0] != "JP misleading" || groups["🇯🇵 日本"] != nil || len(groups["🌐 未分类"]) != 1 || groups["🌐 未分类"][0] != "unknown node" {
		t.Fatalf("unexpected region membership: %v", groups)
	}
	if len(groups["🌍 DE"]) != 1 || groups["🌍 DE"][0] != "German node" || !strings.Contains(strings.Join(groups["☑️ select"], ","), "🌍 DE") || strings.Contains(string(r.Body), "x-vpsct-other-regions") {
		t.Fatal("additional region group missing or metadata leaked")
	}
	b.Template = &domain.RuleTemplate{Kind: "mihomo", Content: "proxy-groups:\n  - name: bad\n    type: select\n    proxies: ['{{all|[}}']\nrules: ['MATCH,bad']\n"}
	if _, err := RenderMihomo(b); err == nil || !strings.Contains(err.Error(), "正则无效") {
		t.Fatalf("invalid regex not rejected: %v", err)
	}
	b.Template.Content = "proxy-groups:\n  - name: JP\n    type: select\n    proxies: ['{{all|JP}}']\nrules: ['MATCH,JP']\n"
	b.Proxies = []proxynode.Proxy{valid("HK only", "HK")}
	if _, err := RenderMihomo(b); err == nil || !strings.Contains(err.Error(), "没有匹配节点") {
		t.Fatalf("missing group target silently changed: %v", err)
	}
	b.Proxies = nil
	b.ValidationOnly = true
	if _, err := RenderMihomo(b); err != nil {
		t.Fatalf("empty syntax validation rejected a future region group: %v", err)
	}
	b.ValidationOnly = false
	if _, err := RenderMihomo(b); err == nil {
		t.Fatal("real empty subscription silently changed a region rule")
	}
}
