package subscription

import (
	"ctlvps/internal/proxynode"
	"strings"
	"testing"
)

func TestSSTCPOnlySubscription(t *testing.T) {
	p := proxynode.Proxy{Name: "SS fixture", Type: "ss", Server: "192.0.2.10", Port: 30001, Params: map[string]any{"cipher": "aes-128-gcm", "password": "fixture", "udp": false}}
	out, ok := SingBoxOutbound(p, "")
	if !ok || out["network"] != "tcp" || out["method"] != "aes-128-gcm" {
		t.Fatal(out)
	}
	for _, line := range []string{SurgeProxyLine(p, ""), ShadowrocketProxyLine(p, "")} {
		if !strings.Contains(line, "udp-relay=false") || strings.Contains(line, "udp-relay=true") {
			t.Fatal(line)
		}
	}
	delete(p.Params, "udp")
	out, _ = SingBoxOutbound(p, "")
	if out["network"] != nil || !strings.Contains(SurgeProxyLine(p, ""), "udp-relay=true") {
		t.Fatal("legacy UDP default changed")
	}
}
