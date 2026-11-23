package nodecombo

import (
	"encoding/json"
	"strings"
	"testing"
)

// Use wire JSON so both protocols exercise the exact input shape clients send.
func TestManagedTLSWireProfiles(t *testing.T) {
	for _, proto := range []string{"vless", "trojan"} {
		t.Run(proto, func(t *testing.T) {
			decode := func(raw string) (string, *WS, *GRPC, error) {
				if proto == "vless" {
					var v VLESS
					if err := json.Unmarshal([]byte(raw), &v); err != nil {
						return "", nil, nil, err
					}
					n, err := v.Normalized()
					return n.Security, n.WS, n.GRPC, err
				}
				var v Trojan
				if err := json.Unmarshal([]byte(raw), &v); err != nil {
					return "", nil, nil, err
				}
				n, err := v.Normalized()
				return n.Security, n.WS, n.GRPC, err
			}
			for _, raw := range []string{`{"transport":"ws","security":"tls"}`, `{"transport":"ws","security":"tls","ws":{"path":"/test","host":"cdn.example.org"}}`, `{"transport":"grpc","security":"tls","grpc":{"service_name":"test.V1_Stream-2"}}`, `{"transport":"tcp","security":"tls"}`} {
				sec, ws, g, err := decode(raw)
				if err != nil || sec != "tls" {
					t.Fatalf("%s: %v", raw, err)
				}
				if strings.Contains(raw, `"ws"`) && (ws == nil || ws.Path == "" || g != nil) || strings.Contains(raw, `"grpc"`) && (g == nil || g.ServiceName != "test.V1_Stream-2" || ws != nil) {
					t.Fatalf("did not recover wire settings: %s", raw)
				}
			}
			for _, raw := range []string{
				`{"transport":"ws"}`, `{"transport":"grpc","security":"reality"}`, `{"transport":"xhttp","security":"tls"}`,
				`{"transport":"tcp","security":"tls","ws":{}}`, `{"transport":"ws","security":"tls","grpc":{}}`, `{"transport":"ws","security":"tls","xhttp":{}}`, `{"transport":"ws","security":"tls","flow":"xtls-rprx-vision"}`,
				`{"transport":"ws","security":"tls","ws":{"path":"/../a"}}`, `{"transport":"ws","security":"tls","ws":{"path":"/a?x"}}`, `{"transport":"ws","security":"tls","ws":{"host":"cdn.example.org:443"}}`, `{"transport":"ws","security":"tls","ws":{"headers":{}}}`,
				`{"transport":"grpc","security":"tls"}`, `{"transport":"grpc","security":"tls","grpc":{"service_name":"/pkg.Test"}}`, `{"transport":"grpc","security":"tls","grpc":{"service_name":"bad\nname"}}`, `{"transport":"grpc","security":"tls","grpc":{"service_name":"` + strings.Repeat("a", 129) + `"}}`, `{"transport":"grpc","security":"tls","grpc":{"service_name":"ok","mode":"multi"}}`,
			} {
				if _, _, _, err := decode(raw); err == nil {
					t.Errorf("accepted %s", raw)
				}
			}
		})
	}
	w := &WS{}
	n, err := (Trojan{Transport: "ws", Security: "tls", WS: w}).Normalized()
	if err != nil || w.Path != "" || n.WS == w || n.WS.Path != "/" {
		t.Fatal("WS defaults must return a detached copy")
	}
}
