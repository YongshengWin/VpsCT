package proxynode

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseSS(t *testing.T) {
	p, err := ParseURI("ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret")) + "@1.2.3.4:8388?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dbing.com#HK%20SS")
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != "ss" || p.Server != "1.2.3.4" || p.Port != 8388 || p.Name != "HK SS" {
		t.Fatalf("%+v", p)
	}
	if p.Str("cipher") != "aes-256-gcm" || p.Str("password") != "secret" || p.Str("plugin") != "obfs" {
		t.Fatalf("%+v", p.Params)
	}
	// legacy form
	legacy := "ss://" + base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:pw@host.example:443")) + "#legacy"
	p2, err := ParseURI(legacy)
	if err != nil || p2.Server != "host.example" || p2.Port != 443 || p2.Str("password") != "pw" {
		t.Fatalf("%v %+v", err, p2)
	}
	// 2022 cipher round trip
	p3 := Proxy{Name: "x", Type: "ss", Server: "h", Port: 1, Params: map[string]any{"cipher": "2022-blake3-aes-128-gcm", "password": "a:b/c="}}
	uri, _ := ToURI(p3)
	back, err := ParseURI(uri)
	if err != nil || back.Str("password") != "a:b/c=" || back.Str("cipher") != "2022-blake3-aes-128-gcm" {
		t.Fatalf("%s -> %v %+v", uri, err, back.Params)
	}
}

func TestVMessRoundTrip(t *testing.T) {
	p := Proxy{Name: "JP VMess", Type: "vmess", Server: "jp.example.com", Port: 443, Params: map[string]any{
		"uuid": "b831381d-6324-4d53-ad4f-8cda48b30811", "alterId": 0, "cipher": "auto", "tls": true, "servername": "jp.example.com",
		"network": "ws", "ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"Host": "cdn.example.com"}},
	}}
	uri, err := ToURI(p)
	if err != nil || !strings.HasPrefix(uri, "vmess://") {
		t.Fatal(err)
	}
	back, err := ParseURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if back.Name != p.Name || back.Str("uuid") != p.Str("uuid") || back.Str("network") != "ws" || !back.Bool("tls") {
		t.Fatalf("%+v", back)
	}
	ws := back.Sub("ws-opts")
	if ws["path"] != "/ws" || ws["headers"].(map[string]any)["Host"] != "cdn.example.com" {
		t.Fatalf("%+v", ws)
	}
}

func TestVLESSReality(t *testing.T) {
	uri := "vless://uuid-1@example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.apple.com&fp=chrome&pbk=PUBKEY&sid=abcd&type=tcp#US%20Reality"
	p, err := ParseURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if p.Str("flow") != "xtls-rprx-vision" || !p.Bool("tls") || p.Str("servername") != "www.apple.com" {
		t.Fatalf("%+v", p.Params)
	}
	ro := p.Sub("reality-opts")
	if ro["public-key"] != "PUBKEY" || ro["short-id"] != "abcd" {
		t.Fatalf("%+v", ro)
	}
	out, _ := ToURI(p)
	back, err := ParseURI(out)
	if err != nil || back.Sub("reality-opts")["public-key"] != "PUBKEY" || back.Name != "US Reality" {
		t.Fatalf("%s -> %v", out, err)
	}
}

func TestTrojanHy2TuicAnyTLSSnell(t *testing.T) {
	cases := []string{
		"trojan://pass@tr.example.com:443?sni=tr.example.com&type=ws&path=%2Ftr&host=tr.example.com&allowInsecure=1#Trojan",
		"hysteria2://pw@hy.example.com:8443?sni=hy.example.com&insecure=1&obfs=salamander&obfs-password=ob#Hy2",
		"hy2://pw@hy.example.com:8443?sni=hy.example.com#Hy2Alias",
		"tuic://uuid:pass@tu.example.com:443?sni=tu.example.com&alpn=h3&congestion_control=bbr#TUIC",
		"anytls://pw@at.example.com:443?sni=at.example.com&insecure=1&fp=chrome#AnyTLS",
		"snell://psk@sn.example.com:9000?version=4&obfs=http&obfs-host=bing.com#Snell",
		"socks5://user:pass@so.example.com:1080#Socks",
		"https://user:pass@ht.example.com:8443#HTTPS",
	}
	for _, c := range cases {
		p, err := ParseURI(c)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		out, err := ToURI(p)
		if err != nil {
			t.Fatalf("%s: encode %v", c, err)
		}
		back, err := ParseURI(out)
		if err != nil {
			t.Fatalf("%s: reparse %v (%s)", c, err, out)
		}
		if back.Type != p.Type || back.Server != p.Server || back.Port != p.Port || back.Name != p.Name {
			t.Fatalf("%s: mismatch %+v vs %+v", c, p, back)
		}
	}
	p, _ := ParseURI(cases[1])
	if p.Str("obfs") != "salamander" || p.Str("obfs-password") != "ob" || !p.Bool("skip-cert-verify") {
		t.Fatalf("%+v", p.Params)
	}
	p, _ = ParseURI(cases[5])
	if p.Int("version") != 4 || p.Sub("obfs-opts")["mode"] != "http" {
		t.Fatalf("%+v", p.Params)
	}
}

func TestParseAnyClashAndBase64(t *testing.T) {
	yamlDoc := `
port: 7890
proxies:
  - {name: A, type: ss, server: a.example.com, port: 443, cipher: aes-128-gcm, password: x}
  - name: B
    type: vless
    server: b.example.com
    port: 443
    uuid: u
    tls: true
    reality-opts: {public-key: k, short-id: s}
proxy-groups:
  - {name: Proxy, type: select, proxies: [A, B]}
`
	res := ParseAny(yamlDoc)
	if res.Format != "clash" || len(res.Proxies) != 2 || res.Proxies[1].Sub("reality-opts")["public-key"] != "k" {
		t.Fatalf("%+v %v", res.Format, res.Errors)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("trojan://p@h.example.com:443#T\nss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:p")) + "@h2.example.com:443#S\n"))
	res = ParseAny(b64)
	if res.Format != "base64" || len(res.Proxies) != 2 {
		t.Fatalf("%s %d %v", res.Format, len(res.Proxies), res.Errors)
	}
	res = ParseAny("garbage://x\ntrojan://p@h.example.com:443#T")
	if len(res.Proxies) != 1 || len(res.Errors) != 1 {
		t.Fatalf("%d %v", len(res.Proxies), res.Errors)
	}
}

func TestDedupeNames(t *testing.T) {
	list := DedupeNames([]Proxy{{Name: "a"}, {Name: "a"}, {Name: "a"}, {Name: "b"}})
	if list[0].Name != "a" || list[1].Name != "a 2" || list[2].Name != "a 3" || list[3].Name != "b" {
		t.Fatalf("%+v", list)
	}
}

// Check the public paths used by single-node creation, bulk paste and external
// sync, including the JSON representation actually handed to storage.
func importLink(t *testing.T, link string) Proxy {
	t.Helper()
	p, err := ParseURI(link)
	if err != nil {
		t.Fatalf("single import: %v", err)
	}
	for _, text := range []string{link, base64.StdEncoding.EncodeToString([]byte(link))} {
		r := ParseAny(text)
		if len(r.Errors) != 0 || len(r.Proxies) != 1 || !reflect.DeepEqual(r.Proxies[0], p) {
			t.Fatalf("batch mismatch: %v", r.Errors)
		}
	}
	return p
}

func TestPublicALPNImports(t *testing.T) {
	for _, tc := range []struct {
		name, link string
		want       []string
	}{
		{"vless", "vless://id@edge.test:443?security=tls&alpn=h2,http/1.1", []string{"h2", "http/1.1"}},
		{"trojan", "trojan://p@edge.test:443?alpn=h2,http/1.1", []string{"h2", "http/1.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := importLink(t, tc.link)
			if _, ok := p.Params["alpn"].([]any); !ok || !reflect.DeepEqual(p.StrList("alpn"), tc.want) || p.Port != 443 {
				t.Fatal("wrong ALPN/port")
			}
			stored := FromDomain(p.ToDomain())
			uri, err := ToURI(stored)
			if err != nil {
				t.Fatal(err)
			}
			back := importLink(t, uri)
			if !reflect.DeepEqual(back.ToDomain().Params, p.ToDomain().Params) {
				t.Fatal("parameters lost after storage and URI round trip")
			}
		})
	}
}

func TestXHTTPClashAndURI(t *testing.T) {
	const doc = `proxies:
  - {name: fixture, type: vless, server: edge.test, port: 443, uuid: id, network: xhttp, xhttp-opts: {path: /rpc, host: cdn.test, mode: stream-up}}`
	r := ParseAny(doc)
	if len(r.Proxies) != 1 || len(r.Errors) != 0 {
		t.Fatal(r.Errors)
	}
	p := FromDomain(r.Proxies[0].ToDomain())
	want := map[string]any{"path": "/rpc", "host": "cdn.test", "mode": "stream-up"}
	if !reflect.DeepEqual(p.ClashMap()["xhttp-opts"], want) {
		t.Fatal("YAML lost XHTTP fields")
	}
	out, err := ToURI(p)
	if err != nil {
		t.Fatal(err)
	}
	back := importLink(t, out)
	if !reflect.DeepEqual(back.Sub("xhttp-opts"), want) {
		t.Fatal("URI lost XHTTP fields")
	}
	for _, bad := range []string{"extra: {unknown: value}", "path: [wrong]", "mode: bogus", "unknown: x"} {
		r := ParseAny(strings.Replace(doc, "path: /rpc, host: cdn.test, mode: stream-up", bad, 1))
		if len(r.Proxies) != 0 || len(r.Errors) != 1 {
			t.Fatalf("accepted unsupported XHTTP option %s", bad)
		}
	}
}

func importLinkSurge(t *testing.T, line string) Proxy {
	t.Helper()
	p, err := ParseURI(line)
	r := ParseAny(line)
	if err != nil || len(r.Errors) != 0 || len(r.Proxies) != 1 || !reflect.DeepEqual(r.Proxies[0], p) {
		t.Fatal("Surge parse failed")
	}
	return p
}

func TestRejectDanglingURIAndPreserveStructuredVia(t *testing.T) {
	for _, link := range []string{"http://edge.test:80?dialer-proxy=front", "vless://id@edge.test:443?type=xhttp&underlying-proxy=front"} {
		if _, err := ParseURI(link); !errors.Is(err, ErrUnsupported) {
			t.Fatal("URI discarded a required front")
		}
	}
	if _, err := ParseSurgeLine("node = trojan, edge.test, 443, password=fixture, underlying-proxy=one, dialer-proxy=two"); err == nil {
		t.Fatal("accepted conflicting front proxies")
	}
	p, err := ParseSurgeLine(`Node = https, edge.test, 443, underlying-proxy="Front, One"`)
	if err != nil || p.Str("dialer-proxy") != "Front, One" {
		t.Fatal("Surge chain reference lost")
	}
	p, err = FromClashMap(p.ClashMap())
	if err != nil || p.Str("dialer-proxy") != "Front, One" {
		t.Fatal("YAML chain reference lost")
	}
	if _, err = ToURI(p); !errors.Is(err, ErrUnsupported) {
		t.Fatal("node link unexpectedly bypassed the front proxy")
	}
}

func TestBasicTLSLinksRejectUnrepresentableHandshake(t *testing.T) {
	for _, prefix := range []string{"vless://fixture@node.example.test:443?security=tls&", "trojan://fixture@node.example.test:443?security=tls&"} {
		for _, q := range []string{"type=grpc&mode=multi", "type=grpc&authority=another.example.test", "type=grpc&serviceName=one&serviceName=two", "type=ws&pcs=fixture", "type=xhttp&pcs=fixture", "type=xhttp&extra=%7B%7D", "type=ws&allowInsecure=true&insecure=false", "type=tcp&serviceName=unexpected"} {
			if _, err := ParseURI(prefix + q); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("lossy import accepted: %s: %v", q, err)
			}
		}
		p := importLink(t, prefix+"type=grpc&mode=gun&serviceName=Fixture.Transport")
		p.Sub("grpc-opts")["authority"] = "another.example.test"
		if _, err := ToURI(p); !errors.Is(err, ErrUnsupported) {
			t.Fatal("lost gRPC authority")
		}
		delete(p.Sub("grpc-opts"), "authority")
		p.Params["fingerprint"] = strings.Repeat("ab", 32)
		if _, err := ToURI(p); !errors.Is(err, ErrUnsupported) {
			t.Fatal("lost certificate pin")
		}
	}
	for _, suffix := range []string{"grpc=true, grpc-service-name=Fixture", "ws-headers=Host:route.example.test", "ws=true, ws-headers=Other:custom", "server-cert-fingerprint-sha256=fixture"} {
		if _, err := ParseSurgeLine("f = trojan, node.example.test, 443, password=fixture, " + suffix); err == nil {
			t.Fatal("unrepresentable native Trojan accepted")
		}
	}
}
