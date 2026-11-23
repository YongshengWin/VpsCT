package nodecombo

import (
	"encoding/json"
	"testing"
)

func TestVLESSNormalized(t *testing.T) {
	for _, mode := range []string{"auto", "packet-up", "stream-up", "stream-one"} {
		x := XHTTP{Mode: mode}
		v, err := (VLESS{Transport: "xhttp", XHTTP: &x}).Normalized()
		if err != nil || v.Security != "reality" || v.Flow != "" || v.XHTTP.Path != "/" || v.XHTTP == &x || x.Path != "" {
			t.Fatalf("mode %q did not produce independent defaults", mode)
		}
	}
	for _, v := range []VLESS{{Security: "tls"}, {Transport: "ws"}, {Transport: "xhttp", Flow: Vision}, {XHTTP: &XHTTP{}}, {Flow: "none"}, {Transport: "xhttp", XHTTP: &XHTTP{Host: "localhost"}}, {Transport: "xhttp", XHTTP: &XHTTP{Host: "127.0.0.1"}}, {Transport: "xhttp", XHTTP: &XHTTP{Mode: "unknown"}}} {
		if _, err := v.Normalized(); err == nil {
			t.Fatal("accepted invalid combination")
		}
	}
	for _, path := range []string{"relative", "/a/../x", "//double", "/query?foo", "/%00", "/a\nb"} {
		if _, err := (VLESS{Transport: "xhttp", XHTTP: &XHTTP{Path: path}}).Normalized(); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
	for _, raw := range []string{`{"transport":"xhttp","encryption":"required"}`, `{"transport":"xhttp","xhttp":{"extra":{}}}`} {
		var v VLESS
		if json.Unmarshal([]byte(raw), &v) == nil {
			t.Fatal("unknown option was discarded")
		}
	}
}
