package proxynode

import (
	"encoding/base64"
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
