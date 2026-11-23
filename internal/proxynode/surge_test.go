package proxynode

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseSurgeSnell(t *testing.T) {
	line := "🇯🇵JP = snell, naiba2.xiaov.uno, 11831, psk = wWvhPWe, version = 5, reuse = true"
	p, err := ParseURI(line)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "🇯🇵JP" || p.Type != "snell" || p.Server != "naiba2.xiaov.uno" || p.Port != 11831 {
		t.Fatalf("identity: %+v", p)
	}
	if p.Str("psk") != "wWvhPWe" || p.Int("version") != 5 || !p.Bool("reuse") {
		t.Fatalf("params: %+v", p.Params)
	}
	res := ParseAny(line)
	if res.Format != "uri-list" || len(res.Proxies) != 1 {
		t.Fatalf("ParseAny: %+v", res)
	}
}

func TestParseSurgeSS(t *testing.T) {
	p, err := ParseSurgeLine("HK = ss, 1.2.3.4, 8388, encrypt-method=aes-256-gcm, password=secret")
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != "ss" || p.Str("cipher") != "aes-256-gcm" || p.Str("password") != "secret" {
		t.Fatalf("%+v", p)
	}
}

func TestModernNativeVLESSImports(t *testing.T) {
	const prefix = "node = vless, node.example.test, 443, username=id, "
	p := importLinkSurge(t, prefix+`sni=cert.test, reality-public-key=key, reality-short-id=abcd, client-fingerprint=chromium-155, alpn="h2,http/1.1", xhttp=true, xhttp-mode=stream-up, xhttp-path=/rpc?sid=1, xhttp-host=cdn.test`)
	if !p.Bool("tls") || p.Bool("skip-cert-verify") || p.Str("client-fingerprint") != "chromium-155" || p.Str("servername") != "cert.test" || !reflect.DeepEqual(p.Sub("reality-opts"), map[string]any{"public-key": "key", "short-id": "abcd"}) || !reflect.DeepEqual(p.Sub("xhttp-opts"), map[string]any{"mode": "stream-up", "path": "/rpc?sid=1", "host": "cdn.test"}) || len(p.StrList("alpn")) != 2 {
		t.Fatal("modern XHTTP/REALITY import incomplete")
	}
	if _, err := ToURI(p); !errors.Is(err, ErrUnsupported) {
		t.Fatal("did not refuse client-specific fingerprint")
	}
	for _, tc := range []struct{ opts, network, param, key, val string }{
		{"tls=true, sni=cert.test, flow=xtls-rprx-vision, block-quic=on", "", "", "flow", "xtls-rprx-vision"},
		{"tls=true, grpc=true, grpc-mode=gun, grpc-service-name=stream.v1", "grpc", "grpc-opts", "grpc-service-name", "stream.v1"},
		{"tls=true, ws=true, ws-path=/stream, ws-headers=Host:cdn.test", "ws", "ws-opts", "path", "/stream"},
		{"ws=true, ws-http-upgrade=true, ws-path=/up", "httpupgrade", "ws-opts", "path", "/up"},
		{"encryption=fixture-encryption, xhttp=true, xhttp-path=/up", "xhttp", "", "encryption", "fixture-encryption"},
	} {
		p := importLinkSurge(t, prefix+tc.opts)
		uri, err := ToURI(p)
		if err != nil {
			t.Fatal(err)
		}
		back := importLink(t, uri)
		for _, n := range []Proxy{p, back} {
			m := n.Params
			if tc.param != "" {
				m = n.Sub(tc.param)
			}
			if n.Str("network") != tc.network || m[tc.key] != tc.val || n.Bool("skip-cert-verify") {
				t.Fatal("transport, flow or encryption lost", tc.network)
			}
		}
	}
	p = importLinkSurge(t, "s = vless, node.test, 443, password=id, obfs=ws, obfs-uri=/ws, obfs-host=cdn.test, encrypt-method=none, tls=true, peer=cert.test, reality=true, pbk=key, sid=12, udp-relay=true")
	if p.Str("uuid") != "id" || p.Sub("ws-opts")["headers"].(map[string]any)["Host"] != "cdn.test" || p.Sub("reality-opts")["short-id"] != "12" {
		t.Fatal("native alias fields lost")
	}
	for _, bad := range []string{"xhttp=true, xhttp-extra=opaque", "xhttp=true, grpc=true", "xhttp-path=/without-xhttp", "grpc=true, tls=true, grpc-mode=multi", "ws=true, ws-headers=Other:value", "tls=false, sni=cert.test", "reality-short-id=12", "ws=maybe", "flow=xtls-rprx-vision, block-quic=off", "password=different"} {
		if _, err := ParseURI(prefix + bad); err == nil {
			t.Fatal("accepted unrepresentable native VLESS", bad)
		}
		res := ParseAny(prefix + bad)
		if len(res.Errors) != 1 || len(res.Proxies) != 0 {
			t.Fatal("batch accepted invalid native option")
		}
	}
}
