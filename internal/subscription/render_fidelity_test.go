package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
	"gopkg.in/yaml.v3"
)

func renderFixture(typ string, params map[string]any) proxynode.Proxy {
	return proxynode.Proxy{Name: "subject", Type: typ, Server: "proxy.example.test", Port: 443, Params: params}
}
func renderOne(t *testing.T, format string, p proxynode.Proxy) *Rendered {
	t.Helper()
	r, e := RenderBundle(&Bundle{Proxies: []proxynode.Proxy{p}}, format)
	if e != nil || r == nil {
		t.Fatalf("%s: %v", format, e)
	}
	if !slices.Equal(r.EmittedNames, []string{p.Name}) {
		t.Fatalf("wrong emitted names: %v", r.EmittedNames)
	}
	return r
}
func requireUnavailable(t *testing.T, b *Bundle, format string) {
	t.Helper()
	r, err := RenderBundle(b, format)
	if r != nil || !errors.Is(err, ErrNoRenderPath) {
		t.Fatalf("%s: expected no deliverable path, got error %v", format, err)
	}
}
func singOutbound(t *testing.T, r *Rendered, tag string) map[string]any {
	t.Helper()
	var d struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(r.Body, &d); err != nil {
		t.Fatal(err)
	}
	for _, ob := range d.Outbounds {
		if ob["tag"] == tag {
			return ob
		}
	}
	t.Fatalf("outbound %q missing", tag)
	return nil
}

func TestRenderTransportFidelity(t *testing.T) {
	const uuid = "11111111-1111-4111-8111-111111111111"
	p := renderFixture("vless", map[string]any{"uuid": uuid, "tls": true, "network": "xhttp", "xhttp-opts": map[string]any{"path": "/stream", "host": "cover.example.test", "mode": "packet-up"}})
	b := &Bundle{Proxies: []proxynode.Proxy{p}}
	for _, f := range []string{FormatSingBox, FormatShadowrocket, FormatSurge} {
		requireUnavailable(t, b, f)
	}
	var mihomo struct{ Proxies []map[string]any }
	if err := yaml.Unmarshal(renderOne(t, FormatMihomo, p).Body, &mihomo); err != nil {
		t.Fatal(err)
	}
	m := mihomo.Proxies[0]
	x := m["xhttp-opts"].(map[string]any)
	if m["network"] != "xhttp" || x["path"] != "/stream" || x["host"] != "cover.example.test" || x["mode"] != "packet-up" {
		t.Fatal("XHTTP handshake changed")
	}
	u, err := url.Parse(strings.TrimSpace(string(renderOne(t, FormatURIList, p).Body)))
	if err != nil || u.Query().Get("type") != "xhttp" || u.Query().Get("path") != "/stream" || u.Query().Get("mode") != "packet-up" {
		t.Fatal("XHTTP link lost transport")
	}
	delete(p.Params, "xhttp-opts")
	p.Params["network"] = "tcp"
	if singOutbound(t, renderOne(t, FormatSingBox, p), p.Name)["type"] != "vless" {
		t.Fatal("normal VLESS lost")
	}
	if !strings.Contains(string(renderOne(t, FormatShadowrocket, p).Body), "obfs=none") {
		t.Fatal("plain VLESS missing")
	}
}

func TestRenderDependentPathsAndPolicyReferences(t *testing.T) {
	good := renderFixture("http", nil)
	good.Name = "healthy"
	bad := renderFixture("vless", map[string]any{"uuid": "11111111-1111-4111-8111-111111111111", "network": "xhttp"})
	bad.Name = "bad-front"
	landing := good
	landing.Name = "sensitive-landing"
	landing.Server = "landing.example.test"
	last := good
	last.Name = "last-hop"
	b := &Bundle{Proxies: []proxynode.Proxy{good, bad}, Chains: []ChainedProxy{{Proxy: last, Via: landing.Name}, {Proxy: landing, Via: bad.Name}}, Groups: []domain.ProxyGroup{{Name: "chosen", Type: "select", Proxies: []string{landing.Name, good.Name, "DIRECT"}}}, Rules: []string{"MATCH,chosen"}}
	for _, f := range []string{FormatSingBox, FormatShadowrocket, FormatSurge, FormatURIList, FormatRaw} {
		t.Run(f, func(t *testing.T) {
			r, e := RenderBundle(b, f)
			if e != nil {
				t.Fatal(e)
			}
			body := r.Body
			if f == FormatRaw {
				body, e = base64.StdEncoding.DecodeString(string(body))
				if e != nil {
					t.Fatal(e)
				}
			}
			if strings.Contains(string(body), landing.Name) || strings.Contains(string(body), landing.Server) || strings.Contains(string(body), last.Name) {
				t.Fatal("dependent path exposed")
			}
			if !slices.Contains(r.EmittedNames, good.Name) || slices.Contains(r.EmittedNames, landing.Name) {
				t.Fatal("wrong projected names")
			}
			if f == FormatSingBox {
				g := singOutbound(t, r, "chosen")["outbounds"].([]any)
				if len(g) != 2 || g[0] != good.Name || g[1] != "direct" {
					t.Fatal("explicit DIRECT or usable member lost")
				}
			}
		})
	}
	for _, f := range []string{FormatRaw, FormatURIList} {
		requireUnavailable(t, &Bundle{Chains: b.Chains, Userinfo: &Userinfo{}, InfoNodes: []string{"metadata"}}, f)
	}
	for _, f := range []string{FormatSingBox, FormatMihomo, FormatSurge, FormatShadowrocket} {
		if _, err := RenderBundle(&Bundle{ValidationOnly: true}, f); err != nil {
			t.Fatalf("%s template preflight: %v", f, err)
		}
		requireUnavailable(t, &Bundle{}, f)
	}
}

func TestRenderRejectsUnavailableRuleTarget(t *testing.T) {
	p := renderFixture("http", nil)
	for format, tpl := range map[string]string{FormatSingBox: `{"outbounds":[],"route":{"final":"missing"}}`, FormatMihomo: "proxy-groups: []\nrules: ['MATCH,missing']", FormatSurge: "[Proxy]\n{{PROXIES}}\n[Rule]\nFINAL,missing", FormatShadowrocket: "[Proxy]\n{{PROXIES}}\n[Rule]\nFINAL,missing"} {
		b := &Bundle{Proxies: []proxynode.Proxy{p}, Template: &domain.RuleTemplate{Kind: format, Content: tpl}}
		if r, err := RenderBundle(b, format); err == nil || r != nil {
			t.Fatalf("%s served unresolved final", format)
		}
		b.Template.Content = strings.ReplaceAll(tpl, "missing", "DIRECT")
		if format == FormatSingBox {
			b.Template.Content = strings.ReplaceAll(tpl, "missing", "direct")
		}
		if _, err := RenderBundle(b, format); err != nil {
			t.Fatalf("%s explicit DIRECT: %v", format, err)
		}
	}
}
