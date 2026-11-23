package provision

import (
	"encoding/json"
	"reflect"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/nodecombo"
)

func tlsTestOptions(proto, network string) Options {
	opt := Options{Protocol: proto, Port: 21443, Domain: "certificate.example.org"}
	var ws *nodecombo.WS
	var grpc *nodecombo.GRPC
	switch network {
	case "ws":
		ws = &nodecombo.WS{Path: "/route/ws", Host: "route.example.org"}
	case "grpc":
		grpc = &nodecombo.GRPC{ServiceName: "echo.Stream"}
	}
	if proto == "vless" {
		opt.VLESS = &nodecombo.VLESS{Transport: network, Security: "tls", WS: ws, GRPC: grpc}
	} else {
		opt.Trojan = &nodecombo.Trojan{Transport: network, Security: "tls", WS: ws, GRPC: grpc}
	}
	return opt
}

func tlsParams(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTLSCredentialsAndTransportRotation(t *testing.T) {
	for _, proto := range []string{"vless", "trojan"} {
		for _, network := range []string{"ws", "grpc", "tcp"} {
			for _, mode := range []string{"self_signed", "external", "acme"} {
				t.Run(proto+"/"+network+"/"+mode, func(t *testing.T) {
					server := domain.Server{ID: 7, CoreMode: domain.CoreModeLean, PublicHost: "access.example.org", CertMode: mode}
					opts := tlsTestOptions(proto, network)
					if mode == "external" {
						opts.CertID = "local-cert"
					}
					n, err := NewNode(server, "", opts)
					if err != nil {
						t.Fatal(err)
					}
					client, srv := tlsParams(t, n.Params), tlsParams(t, n.ServerParams)
					if n.Core != domain.CoreSingBox || n.Server != server.PublicHost || srv["tls_domain"] != opts.Domain || srv["cert_mode"] != mode || srv["network"] != network || srv["security"] != "tls" || client["network"] != network {
						t.Fatal("incorrect TLS binding or transport")
					}
					if hasAny(client, "cert_id", "cert_mode", "reality-opts", "flow") || hasAny(srv, "flow", "handshake_server", "reality_private_key", "reality_public_key", "reality_short_id") {
						t.Fatal("TLS profile contains private or REALITY options")
					}
					if mode == "external" && srv["cert_id"] != opts.CertID || mode != "external" && hasAny(srv, "cert_id") {
						t.Fatal("wrong certificate reference")
					}
					if mode == "self_signed" && client["skip-cert-verify"] != true || mode != "self_signed" && hasAny(client, "skip-cert-verify") {
						t.Fatal("wrong certificate verification policy")
					}
					key := "password"
					if proto == "vless" {
						key = "uuid"
						combo, err := VLESSFromNode(n)
						if err != nil || !reflect.DeepEqual(combo, opts.VLESS) || client["tls"] != true || client["servername"] != opts.Domain {
							t.Fatal("VLESS TLS profile mismatch", err)
						}
					} else {
						combo, err := TrojanFromNode(n)
						if err != nil || !reflect.DeepEqual(combo, opts.Trojan) || client["sni"] != opts.Domain {
							t.Fatal("Trojan TLS profile mismatch", err)
						}
					}
					switch network {
					case "ws":
						if srv["ws_path"] != "/route/ws" || srv["ws_host"] != "route.example.org" || !reflect.DeepEqual(client["ws-opts"], map[string]any{"path": "/route/ws", "headers": map[string]any{"Host": "route.example.org"}}) {
							t.Fatal("incorrect WS mapping")
						}
					case "grpc":
						if srv["grpc_service_name"] != "echo.Stream" || !reflect.DeepEqual(client["grpc-opts"], map[string]any{"grpc-service-name": "echo.Stream"}) {
							t.Fatal("incorrect gRPC mapping")
						}
					}
					if err = RegenerateCredentials(&n, server); err != nil {
						t.Fatal(err)
					}
					freshC, freshS := tlsParams(t, n.Params), tlsParams(t, n.ServerParams)
					if client[key] == nil || client[key] != srv[key] || freshC[key] == nil || freshC[key] != freshS[key] || client[key] == freshC[key] {
						t.Fatal("credential did not rotate on both sides")
					}
					delete(client, key)
					delete(srv, key)
					delete(freshC, key)
					delete(freshS, key)
					if !reflect.DeepEqual(client, freshC) || !reflect.DeepEqual(srv, freshS) {
						t.Fatal("rotation changed non-credential settings")
					}
				})
			}
		}
	}
}

func TestTLSRejectsConflictingInputsAndCorruptState(t *testing.T) {
	server := domain.Server{CoreMode: domain.CoreModeStable, PublicHost: "192.0.2.1"}
	for _, proto := range []string{"trojan", "vless"} {
		for _, mutate := range []func(*Options){
			func(o *Options) { o.Protocol = "anytls" }, func(o *Options) { o.CertMode = "none" }, func(o *Options) { o.CertMode = "acme"; o.Domain = "" }, func(o *Options) { o.Domain = "https://route.example.org" }, func(o *Options) { o.SNI = "other.example.org" }, func(o *Options) { o.CertMode = "external" }, func(o *Options) { o.CertMode = "external"; o.CertID = "../cert" }, func(o *Options) { o.CertID = "ignored" },
		} {
			o := tlsTestOptions(proto, "grpc")
			mutate(&o)
			if _, err := NewNode(server, "", o); err == nil {
				t.Errorf("%s accepted invalid input", proto)
			}
		}
		for _, mutate := range []func(map[string]any, map[string]any){
			func(s, c map[string]any) { delete(s, "security") }, func(s, c map[string]any) { s["ws_path"] = "/unused" }, func(s, c map[string]any) { c["grpc-opts"] = map[string]any{"grpc-service-name": "other"} }, func(s, c map[string]any) { s["reality_short_id"] = "wrong" }, func(s, c map[string]any) { c["flow"] = nodecombo.Vision }, func(s, c map[string]any) { s["tls_domain"] = "other.example.org" }, func(s, c map[string]any) { c["skip-cert-verify"] = false },
		} {
			n, err := NewNode(server, "", tlsTestOptions(proto, "grpc"))
			if err != nil {
				t.Fatal(err)
			}
			s, c := tlsParams(t, n.ServerParams), tlsParams(t, n.Params)
			mutate(s, c)
			n.ServerParams, _ = json.Marshal(s)
			n.Params, _ = json.Marshal(c)
			old := n
			if RegenerateCredentials(&n, server) == nil || !reflect.DeepEqual(n, old) {
				t.Errorf("%s rotated corrupt state", proto)
			}
		}
	}
}

func TestNewTLSProfilesRejectNetworkBinding(t *testing.T) {
	server := domain.Server{CoreMode: domain.CoreModeStable, PublicHost: "access.example.org"}
	binding := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}
	for _, proto := range []string{"vless", "trojan"} {
		for _, network := range []string{"ws", "grpc", "tcp"} {
			opts := tlsTestOptions(proto, network)
			opts.Network = binding
			_, err := NewNode(server, "", opts)
			native := proto == "trojan" && network == "tcp"
			if (err == nil) != native {
				t.Errorf("%s/%s network binding: %v", proto, network, err)
			}
		}
	}
}

func TestUnmarkedTrojanRemainsNativeAfterRotation(t *testing.T) {
	server := domain.Server{CoreMode: domain.CoreModeStable, PublicHost: "192.0.2.1"}
	n, err := NewNode(server, "", Options{Port: 23443, Protocol: "trojan"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := TrojanFromNode(n)
	if err != nil || *v != (nodecombo.Trojan{Transport: "tcp", Security: "tls"}) {
		t.Fatal("native Trojan not recognized", err)
	}
	old := tlsParams(t, n.Params)["password"]
	if err = RegenerateCredentials(&n, server); err != nil {
		t.Fatal(err)
	}
	if hasAny(tlsParams(t, n.ServerParams), "network", "security") || hasAny(tlsParams(t, n.Params), "network") || old == tlsParams(t, n.Params)["password"] {
		t.Fatal("native Trojan storage or rotation changed")
	}
}
