package subscription

import (
	"crypto/ecdh"
	"crypto/rand"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/wgconfig"
	"encoding/base64"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestTunnelClientsRoundTripAndFiltering(t *testing.T) {
	a, _ := ecdh.X25519().GenerateKey(rand.Reader)
	b, _ := ecdh.X25519().GenerateKey(rand.Reader)
	b64 := base64.StdEncoding.EncodeToString
	p, err := proxynode.FromClashMap(map[string]any{"name": "wg-fixture", "type": "wireguard", "server": "wg.example.test", "port": 51820, "ip": "10.99.0.2", "private-key": b64(a.Bytes()), "public-key": b64(b.PublicKey().Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	c, err := wgconfig.Decode(p.Params)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := c.StandardConfig(p.Server, p.Port)
	if err != nil {
		t.Fatal(err)
	}
	tcpOnly := c
	tcpOnly.UDP = false
	if _, e := tcpOnly.StandardConfig(p.Server, p.Port); e == nil {
		t.Fatal("TCP-only restriction silently dropped by standard export")
	}
	parsed := proxynode.ParseAny(standard)
	if len(parsed.Proxies) != 1 || len(parsed.Errors) > 0 {
		t.Fatal(parsed.Errors)
	}
	if result := proxynode.ParseAny(strings.Replace(standard, "[Peer]", "PostUp = touch /tmp/unsafe\n[Peer]", 1)); len(result.Proxies) != 0 {
		t.Fatal("WG hook imported")
	}
	if _, err := proxynode.FromClashMap(map[string]any{"name": "bad", "type": "mieru", "server": "x", "port": 2999, "username": "x", "password": "x", "port-range": "2000-3000"}); err == nil {
		t.Fatal("mieru range silently discarded")
	}
	m, err := proxynode.FromClashMap(map[string]any{"name": "mieru-fixture", "type": "mieru", "server": "mieru.example.test", "port": 2999, "username": "fixture", "password": "fixture-only", "transport": "UDP"})
	if err != nil {
		t.Fatal(err)
	}
	bundle := &Bundle{Name: "tunnel-fixture", Proxies: []proxynode.Proxy{p, m}}
	got, err := RenderMihomo(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var clash map[string]any
	if yaml.Unmarshal(got.Body, &clash) != nil || len(clash["proxies"].([]any)) != 2 {
		t.Fatal("client types lost")
	}
	got, err = RenderSingBox(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if json.Unmarshal(got.Body, &doc) != nil {
		t.Fatal("invalid JSON")
	}
	endpoints, ok := doc["endpoints"].([]any)
	if !ok || len(endpoints) != 1 {
		t.Fatal("WG endpoint missing")
	}
	if endpoints[0].(map[string]any)["private_key"] != c.PrivateKey {
		t.Fatal("WG key changed")
	}
	if strings.Contains(string(got.Body), "mieru-fixture") || strings.Contains(string(got.Body), "fixture-only") {
		t.Fatal("unsupported mieru leaked into sing-box selectors")
	}
	p.Params["remote-dns-resolve"] = true
	p.Params["dns"] = []string{"10.99.0.1"}
	if _, ok := SingBoxEndpoint(p, ""); ok {
		t.Fatal("non-equivalent remote DNS silently converted")
	}
}
