package subscription

import (
	"strings"
	"testing"

	"ctlvps/internal/proxynode"
)

func TestShadowrocketImportedSnell(t *testing.T) {
	for _, input := range []string{
		`Snell = snell, snell.example.com, 9000, psk=test-secret, version=4, obfs=http, obfs-host=example.com, reuse=true, tfo=true`,
		`proxies:
  - {name: Snell, type: snell, server: snell.example.com, port: 9000, psk: test-secret, version: 4, obfs-opts: {mode: http, host: example.com}, reuse: true, tfo: true, udp: true}`,
	} {
		parsed := proxynode.ParseAny(input)
		if len(parsed.Errors) != 0 || len(parsed.Proxies) != 1 {
			t.Fatalf("import failed: %+v", parsed)
		}
		p := parsed.Proxies[0]
		chain := p
		chain.Name = "Snell chain"
		r, err := RenderShadowrocket(&Bundle{Name: "test", Proxies: parsed.Proxies, Chains: []ChainedProxy{{Proxy: chain, Via: p.Name}}})
		if err != nil {
			t.Fatal(err)
		}
		body := string(r.Body)
		for _, want := range []string{
			"Snell = snell, snell.example.com, 9000, password=test-secret, version=4",
			"Snell chain = snell, snell.example.com, 9000, password=test-secret, version=4",
			"obfs=http", "obfs-host=example.com", "udp=1", `underlying-proxy="Snell"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(body, "psk=") {
			t.Error("Shadowrocket must use its native password field")
		}
		if !strings.Contains(SurgeProxyLine(p, ""), "psk=test-secret") || p.Str("psk") != "test-secret" {
			t.Error("Shadowrocket rendering changed Surge output or stored credentials")
		}
	}
}

func TestShadowrocketSnellOptions(t *testing.T) {
	p := proxynode.Proxy{Name: "Snell", Type: "snell", Server: "2001:db8::1", Port: 443, Params: map[string]any{"psk": "test,secret", "version": 5, "reuse": true, "tfo": true}}
	line := ShadowrocketProxyLine(p, "")
	if !strings.Contains(line, `password="test,secret", version=5`) {
		t.Fatalf("password quoting or explicit version lost: %s", line)
	}
	if !strings.Contains(line, "reuse=true, tfo=true") || strings.Contains(line, "udp=1") {
		t.Fatalf("optional flags changed: %s", line)
	}
	delete(p.Params, "version")
	if line := ShadowrocketProxyLine(p, ""); !strings.Contains(line, "version=4") || strings.Contains(line, "<nil>") {
		t.Fatalf("invalid default Snell options: %s", line)
	}
}
