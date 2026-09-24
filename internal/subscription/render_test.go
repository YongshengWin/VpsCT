package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
)

func sampleBundle() *Bundle {
	proxies := []proxynode.Proxy{
		{Name: "HK Reality", Type: "vless", Server: "hk.example.com", Port: 443, Params: map[string]any{"uuid": "11111111-1111-1111-1111-111111111111", "flow": "xtls-rprx-vision", "tls": true, "servername": "www.apple.com", "client-fingerprint": "chrome", "reality-opts": map[string]any{"public-key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "short-id": "ab"}}},
		{Name: "JP Hy2", Type: "hysteria2", Server: "jp.example.com", Port: 8443, Params: map[string]any{"password": "pw", "sni": "jp.example.com", "skip-cert-verify": true, "up": "50 Mbps", "down": "200 Mbps"}},
		{Name: "US Snell", Type: "snell", Server: "us.example.com", Port: 9000, Params: map[string]any{"psk": "psk", "version": 4}},
		{Name: "SG AnyTLS", Type: "anytls", Server: "sg.example.com", Port: 443, Params: map[string]any{"password": "pw", "sni": "sg.example.com"}},
		{Name: "TW SS", Type: "ss", Server: "tw.example.com", Port: 8388, Params: map[string]any{"cipher": "2022-blake3-aes-128-gcm", "password": "AAAAAAAAAAAAAAAAAAAAAA=="}},
	}
	chains := []ChainedProxy{{Proxy: proxynode.Proxy{Name: "HK → US 落地", Type: "ss", Server: "landing.example.com", Port: 8388, Params: map[string]any{"cipher": "aes-128-gcm", "password": "x"}}, Via: "HK Reality"}}
	groups := ResolveGroups([]domain.ProxyGroup{
		{Name: "节点选择", Type: "select", Proxies: []string{"自动选择", "DIRECT"}, IncludeAll: true},
		{Name: "自动选择", Type: "url-test", Filter: "HK|JP"},
		{Name: "落地", Type: "select", Filter: "落地"},
	}, proxies, chains, nil)
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ui := &Userinfo{Upload: 1 << 30, Download: 5 << 30, Total: 100 << 30, Expire: &exp}
	return &Bundle{Name: "demo", Proxies: proxies, Chains: chains, Groups: groups, Userinfo: ui, InfoNodes: ui.InfoLines(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Rules: []string{"GEOSITE,cn,DIRECT", "GEOIP,CN,DIRECT", "DOMAIN-SUFFIX,openai.com,节点选择", "DOMAIN-SUFFIX,ads.example,REJECT", "MATCH,节点选择"}}
}

func TestResolveGroups(t *testing.T) {
	b := sampleBundle()
	if len(b.Groups) != 3 {
		t.Fatalf("groups: %d", len(b.Groups))
	}
	sel := b.Groups[0]
	if sel.Proxies[0] != "自动选择" || sel.Proxies[1] != "DIRECT" || len(sel.Proxies) != 2+6 {
		t.Fatalf("select members: %v", sel.Proxies)
	}
	auto := b.Groups[1]
	if len(auto.Proxies) != 3 || auto.URL == "" || auto.Interval == 0 { // HK Reality, JP Hy2, HK → US 落地
		t.Fatalf("url-test: %+v", auto)
	}
	if b.Groups[2].Proxies[0] != "HK → US 落地" {
		t.Fatalf("chain group: %v", b.Groups[2].Proxies)
	}
}

func TestRenderMihomo(t *testing.T) {
	r, err := RenderMihomo(sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(r.Body, &doc); err != nil {
		t.Fatalf("output not yaml: %v\n%s", err, r.Body)
	}
	proxies := doc["proxies"].([]any)
	if len(proxies) != 5+1 { // proxies + chain; info lines stay in the userinfo header
		t.Fatalf("proxies: %d", len(proxies))
	}
	last := proxies[len(proxies)-1].(map[string]any)
	if last["dialer-proxy"] != "HK Reality" {
		t.Fatalf("chain should carry dialer-proxy: %v", last)
	}
	first := proxies[0].(map[string]any)
	if first["name"] != "HK Reality" {
		t.Fatalf("first proxy should be a real node, not an info placeholder: %v", first)
	}
	if _, ok := doc["rule-providers"]; ok {
		t.Fatal("custom GEOSITE rules must not keep template rule-providers")
	}
	groups := doc["proxy-groups"].([]any)
	if len(groups) != 3 {
		t.Fatalf("groups: %d", len(groups))
	}
	rules := doc["rules"].([]any)
	if rules[len(rules)-1] != "MATCH,节点选择" {
		t.Fatalf("rules: %v", rules)
	}
	if _, ok := doc["dns"]; !ok {
		t.Fatal("template dns section missing")
	}
}

func TestRenderMihomoFiltersUnsupportedSnell(t *testing.T) {
	for _, templateGroups := range []bool{false, true} {
		t.Run(fmt.Sprint(templateGroups), func(t *testing.T) {
			b := sampleBundle()
			bad := proxynode.Proxy{Name: "unsupported", Type: "snell", Server: "example.com", Port: 443, Params: map[string]any{"version": "6", "psk": "test"}}
			b.Proxies = append(b.Proxies, bad)
			b.Chains = append(b.Chains,
				ChainedProxy{Proxy: proxynode.Proxy{Name: "dependent-two", Type: "ss"}, Via: "dependent-one"},
				ChainedProxy{Proxy: proxynode.Proxy{Name: "dependent-one", Type: "ss"}, Via: "unsupported"},
				ChainedProxy{Proxy: proxynode.Proxy{Name: "unsupported-chain", Type: "snell", Params: map[string]any{"version": 6}}, Via: "HK Reality"})
			b.Groups = []domain.ProxyGroup{{Name: "selection", Type: "select", Proxies: []string{"unsupported", "dependent-one", "dependent-two", "unsupported-chain"}}}
			b.Rules = []string{"DOMAIN,example.com,unsupported", "GEOIP,CN,dependent-one,no-resolve", "MATCH,selection"}
			if templateGroups {
				b.Groups = nil
				b.Rules = nil
				b.Template = &domain.RuleTemplate{Kind: "mihomo", Content: "proxy-groups:\n  - name: selection\n    type: select\n    proxies: [unsupported, dependent-one, dependent-two, unsupported-chain]\n  - name: all\n    type: select\n    proxies: ['{{all}}']\nrules: ['DOMAIN,example.com,unsupported', 'MATCH,selection']\n"}
			}
			r, err := RenderMihomo(b)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"unsupported", "dependent-one", "dependent-two", "unsupported-chain"} {
				if strings.Contains(string(r.Body), name) {
					t.Fatalf("dangling reference to %s: %s", name, r.Body)
				}
			}
			var doc map[string]any
			if err := yaml.Unmarshal(r.Body, &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc["proxies"].([]any)) != 6 {
				t.Fatal("compatible nodes were removed")
			}
			members := doc["proxy-groups"].([]any)[0].(map[string]any)["proxies"].([]any)
			if len(members) != 1 || members[0] != "DIRECT" {
				t.Fatalf("empty group fallback: %v", members)
			}
			if len(b.Proxies) != 6 || len(b.Chains) != 4 || b.Proxies[5].Int("version") != 6 {
				t.Fatal("render mutated source bundle")
			}
		})
	}
}

func TestMihomoSnellVersionCompatibility(t *testing.T) {
	for _, version := range []any{nil, 0, 1, 2, 3, 4, 5, 6, float64(6), "6", -1} {
		p := proxynode.Proxy{Name: "snell", Type: "snell", Params: map[string]any{"version": version}}
		b, _ := mihomoCompatibleBundle(&Bundle{Proxies: []proxynode.Proxy{p}})
		want := p.Int("version") >= 0 && p.Int("version") <= 5
		if (len(b.Proxies) == 1) != want {
			t.Fatalf("version %v: kept %d proxies", version, len(b.Proxies))
		}
	}
}

func TestRenderMihomoTemplateMarkers(t *testing.T) {
	b := sampleBundle()
	b.Groups = nil
	b.Rules = nil
	r, err := RenderMihomo(b)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = yaml.Unmarshal(r.Body, &doc)
	groups := doc["proxy-groups"].([]any)
	byName := map[string]map[string]any{}
	for _, g := range groups {
		m := g.(map[string]any)
		byName[fmt.Sprint(m["name"])] = m
	}
	smart := byName["⚡️ smart"]
	if smart == nil {
		t.Fatalf("missing ⚡️ smart: %v", byName)
	}
	members := smart["proxies"].([]any)
	if len(members) != 6 { // all proxies via {{all}}
		t.Fatalf("smart marker expansion: %v", members)
	}
	hk := byName["🇭🇰 香港"]
	if hk == nil {
		t.Fatal("missing 🇭🇰 香港")
	}
	hkMembers := hk["proxies"].([]any)
	if len(hkMembers) != 2 { // HK Reality, HK → US 落地
		t.Fatalf("hk filter: %v", hkMembers)
	}
	if _, ok := doc["rule-providers"]; !ok {
		t.Fatal("rule-providers missing")
	}
	rules := doc["rules"].([]any)
	if rules[len(rules)-1] != "MATCH,🇺🇸 美国" {
		t.Fatalf("template final: %v", rules[len(rules)-1])
	}
}

func TestRenderSingBoxTemplateGroups(t *testing.T) {
	b := sampleBundle()
	b.Groups = nil
	b.Rules = nil
	r, err := RenderSingBox(b)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(r.Body, &doc); err != nil {
		t.Fatal(err)
	}
	tags := map[string]map[string]any{}
	for _, o := range doc["outbounds"].([]any) {
		m := o.(map[string]any)
		tags[m["tag"].(string)] = m
	}
	if tags["⚡️ smart"]["type"] != "urltest" {
		t.Fatalf("smart: %v", tags["⚡️ smart"])
	}
	smartOut := tags["⚡️ smart"]["outbounds"].([]any)
	if len(smartOut) != 5 { // snell skipped
		t.Fatalf("smart members: %v", smartOut)
	}
	hk := tags["🇭🇰 香港"]["outbounds"].([]any)
	if len(hk) != 2 {
		t.Fatalf("hk members: %v", hk)
	}
	route := doc["route"].(map[string]any)
	if route["final"] != "🇺🇸 美国" {
		t.Fatalf("final: %v", route["final"])
	}
	dns := doc["dns"].(map[string]any)
	for _, s := range dns["servers"].([]any) {
		m := s.(map[string]any)
		if d, ok := m["detour"].(string); ok && d != "" && d != "direct" {
			if _, exists := tags[d]; !exists {
				t.Fatalf("dns detour %q missing", d)
			}
		}
	}
}

func TestRenderRawAndSurge(t *testing.T) {
	b := sampleBundle()
	r, err := RenderRaw(b, false)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := base64.StdEncoding.DecodeString(string(r.Body))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(dec)), "\n")
	if len(lines) != 1+5+1 || !strings.HasPrefix(lines[1], "vless://") || !strings.HasPrefix(lines[3], "snell://") {
		t.Fatalf("raw lines: %v", lines)
	}
	s, err := RenderSurge(b)
	if err != nil {
		t.Fatal(err)
	}
	body := string(s.Body)
	for _, want := range []string{
		"US Snell = snell, us.example.com, 9000, psk=psk, version=4",
		"[Proxy]",
		"🧩 国内直连 = direct",
		"⛔️ 拦截净化 = reject",
		"[Rule]",
		"FINAL,DIRECT",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("surge missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "vless") || strings.Contains(body, "HK Reality =") {
		t.Fatal("Surge must omit vless; unknown type fails the whole #!include")
	}
	if strings.Contains(body, `underlying-proxy="HK Reality"`) {
		t.Fatal("chain via omitted vless front must also be omitted")
	}
	if strings.Contains(body, "#!name=") {
		t.Fatal("default surge must not use #!name; that breaks #!include into an existing profile")
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "[Proxy]") {
		t.Fatal("detached #!include requires the file to start with [Proxy]")
	}
	for _, drop := range []string{"[General]", "[Proxy Group]", "[Host]", "⚡️ smart", "RULE-SET", "节点选择 = select"} {
		if strings.Contains(body, drop) {
			t.Fatalf("default surge should be a proxy list, still has %q in:\n%s", drop, body)
		}
	}
}

func TestRenderSurgeCustomTemplate(t *testing.T) {
	b := sampleBundle()
	b.Template = &domain.RuleTemplate{Kind: "surge", Content: `[Proxy]
{{PROXIES}}

[Proxy Group]
{{PROXY_GROUPS}}

[Rule]
{{RULES}}
FINAL,DIRECT
`}
	s, err := RenderSurge(b)
	if err != nil {
		t.Fatal(err)
	}
	body := string(s.Body)
	for _, want := range []string{
		"节点选择 = select, 自动选择, DIRECT",
		"DOMAIN-SUFFIX,openai.com,节点选择",
		"FINAL,DIRECT",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("custom surge missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "GEOSITE") {
		t.Fatal("GEOSITE rules must be dropped for Surge")
	}
	if strings.Contains(body, "FINAL,节点选择") {
		t.Fatal("subscription FINAL must yield to the template FINAL so RULE-SETs stay reachable")
	}
}

func TestRenderShadowrocket(t *testing.T) {
	r, err := RenderShadowrocket(sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	body := string(r.Body)
	for _, want := range []string{
		"[General]",
		"yaml = true",
		"skip-proxy = 192.168.0.0/16",
		"DOMAIN-SUFFIX,themoviedb.org",
		"HK Reality = vless, hk.example.com, 443",
		"reality=true",
		"节点选择 = select",
		"FINAL,🇺🇸 美国",
		"[URL Rewrite]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("shadowrocket missing %q in:\n%s", want, body)
		}
	}
	for _, drop := range []string{"update-url", "{{all}}", "{{PROXIES}}", "vless-reality", "IP-CIDR,"} {
		if strings.Contains(body, drop) {
			t.Fatalf("shadowrocket should not contain %q", drop)
		}
	}
	empty, err := RenderShadowrocket(&Bundle{Name: "check"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty.Body), "DIRECT") {
		t.Fatal("empty bundle must still render")
	}
	onlyNodes := &Bundle{Name: "nodes", Proxies: sampleBundle().Proxies}
	got, err := RenderShadowrocket(onlyNodes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.Body), "⚡️ smart = url-test,HK Reality") {
		t.Fatalf("template groups should expand {{all}}:\n%s", got.Body)
	}
}

func TestRenderSingBox(t *testing.T) {
	r, err := RenderSingBox(sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(r.Body, &doc); err != nil {
		t.Fatal(err)
	}
	outbounds := doc["outbounds"].([]any)
	tags := map[string]map[string]any{}
	for _, o := range outbounds {
		m := o.(map[string]any)
		tags[m["tag"].(string)] = m
	}
	if _, ok := tags["US Snell"]; ok {
		t.Fatal("snell has no sing-box outbound and must be skipped")
	}
	if tags["HK → US 落地"]["detour"] != "HK Reality" {
		t.Fatalf("chain detour: %v", tags["HK → US 落地"])
	}
	if tags["节点选择"]["type"] != "selector" || tags["自动选择"]["type"] != "urltest" {
		t.Fatalf("groups: %v %v", tags["节点选择"], tags["自动选择"])
	}
	route := doc["route"].(map[string]any)
	if route["final"] != "节点选择" {
		t.Fatalf("final: %v", route["final"])
	}
	if sb, err := exec.LookPath("sing-box"); err == nil {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, r.Body, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(sb, "check", "-c", path).CombinedOutput()
		if err != nil {
			t.Fatalf("sing-box check failed: %v\n%s\n%s", err, out, r.Body)
		}
	}
}

// GEOSITE/GEOIP rules whose rule_set is not declared by the template must be
// kept by synthesising a remote .srs declaration, not silently dropped.
func TestRenderSingBoxAutoRuleSets(t *testing.T) {
	b := sampleBundle()
	b.Rules = []string{"GEOSITE,openai,节点选择", "GEOIP,telegram,节点选择,no-resolve", "GEOSITE,cn,DIRECT", "MATCH,节点选择"}
	r, err := RenderSingBox(b)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Route struct {
			RuleSet []map[string]any `json:"rule_set"`
			Rules   []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(r.Body, &doc); err != nil {
		t.Fatal(err)
	}
	sets := map[string]string{}
	cnTags := 0
	for _, rs := range doc.Route.RuleSet {
		tag := rs["tag"].(string)
		sets[tag] = fmt.Sprint(rs["url"])
		if tag == "geosite-cn" {
			cnTags++
		}
	}
	if !strings.Contains(sets["geosite-openai"], "sing-geosite/rule-set/geosite-openai.srs") {
		t.Fatalf("geosite-openai not declared: %v", sets)
	}
	if !strings.Contains(sets["geoip-telegram"], "sing-geoip/rule-set/geoip-telegram.srs") {
		t.Fatalf("geoip-telegram not declared: %v", sets)
	}
	if cnTags != 1 {
		t.Fatalf("template-declared geosite-cn duplicated: %d", cnTags)
	}
	found := false
	for _, rule := range doc.Route.Rules {
		if l, ok := rule["rule_set"].([]any); ok {
			for _, x := range l {
				if x == "geosite-openai" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("openai rule dropped: %v", doc.Route.Rules)
	}
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]string{
		"Shadowrocket/2.2.40 CFNetwork": FormatShadowrocket,
		"Surge iOS/3211":                FormatSurge,
		"clash-verge/v2.0.0":            FormatMihomo,
		"ClashMetaForAndroid/2.11":      FormatMihomo,
		"sing-box 1.11.0":               FormatSingBox,
		"v2rayNG/1.9.0":                 FormatRaw,
		"v2rayU/4.2.8":                  FormatRaw,
		"V2RayX/1.0":                    FormatRaw,
		"Mozilla/5.0":                   "",
	}
	for ua, want := range cases {
		if got := DetectFormat(ua); got != want {
			t.Errorf("%s: got %s want %s", ua, got, want)
		}
	}
}

func TestShareUserinfoDual(t *testing.T) {
	sh := domain.Share{UsedUpload: 100, UsedDownload: 200, QuotaBytes: 1000, BillingMode: domain.BillingSum}
	ui := shareUserinfo(sh)
	if ui.Upload != 100 || ui.Download != 200 || ui.Remaining() != 700 {
		t.Fatalf("sum: %+v rem=%d", ui, ui.Remaining())
	}
	sh.BillingMode = domain.BillingDual
	ui = shareUserinfo(sh)
	if ui.Upload != 100 || ui.Download != 200 || ui.Remaining() != 700 {
		t.Fatalf("dual: %+v rem=%d", ui, ui.Remaining())
	}
}

func TestUserinfo(t *testing.T) {
	ui, ok := ParseUserinfo("upload=100; download=200; total=1000; expire=1893456000")
	if !ok || ui.Upload != 100 || ui.Download != 200 || ui.Total != 1000 || ui.Expire == nil {
		t.Fatalf("%+v %v", ui, ok)
	}
	if ui.Remaining() != 700 {
		t.Fatal("remaining")
	}
	if !strings.Contains(ui.Header(), "expire=1893456000") {
		t.Fatal(ui.Header())
	}
}

func TestValidateGroups(t *testing.T) {
	errs := ValidateGroups([]domain.ProxyGroup{{Name: "A", Proxies: []string{"B"}}, {Name: "B", Proxies: []string{"A"}}})
	if len(errs) == 0 || !strings.Contains(errs[0], "循环") {
		t.Fatalf("%v", errs)
	}
	errs = ValidateGroups([]domain.ProxyGroup{{Name: "A", Filter: "("}})
	if len(errs) != 1 {
		t.Fatalf("%v", errs)
	}
}

func TestSurgeAnyTLSChainLine(t *testing.T) {
	p := proxynode.Proxy{Name: "🔗 HK-Vmiss+Zouter", Type: "anytls", Server: "zouter.example", Port: 21000, Params: map[string]any{"password": "pw"}}
	line := SurgeProxyLine(p, "🛤 HK-Vmiss")
	want := `🔗 HK-Vmiss+Zouter = anytls, zouter.example, 21000, password=pw, underlying-proxy="🛤 HK-Vmiss", sni=zouter.example, test-timeout=8`
	if line != want {
		t.Fatalf("got:\n%s\nwant:\n%s", line, want)
	}
	if got := SurgeProxyLine(proxynode.Proxy{Name: "JP VLESS", Type: "vless", Server: "jp.example", Port: 443}, ""); got != "" {
		t.Fatalf("vless must be omitted for Surge, got %q", got)
	}
}
