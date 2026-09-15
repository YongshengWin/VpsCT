package proxynode

import "testing"

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
