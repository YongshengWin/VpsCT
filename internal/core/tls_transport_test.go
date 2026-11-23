package core

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
)

func TestManagedTLSTransportConfig(t *testing.T) {
	for _, protocol := range []string{"vless", "trojan"} {
		for _, net := range []string{"tcp", "ws", "grpc"} {
			if protocol == "trojan" && net == "tcp" {
				continue
			}
			for _, mode := range []string{"self_signed", "external", "acme"} {
				t.Run(protocol+"/"+net+"/"+mode, func(t *testing.T) {
					dir := t.TempDir()
					d := NewSingBox(Paths{DataDir: dir, CertDir: dir, LogDir: dir}, NewSystemd())
					ds := &agentproto.DesiredState{TLSTransportVersion: 1, Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: corecompat.NetworkBaseline}}}
					p := map[string]any{"network": net, "security": "tls", "tls_domain": "origin.example.org", "cert_mode": mode}
					user := map[string]any{}
					if protocol == "vless" {
						p["uuid"] = "00000000-0000-4000-8000-000000000001"
						user["uuid"] = p["uuid"]
					} else {
						p["password"] = "fixture"
						user["password"] = p["password"]
					}
					var tr any
					var alpn []string
					switch net {
					case "ws":
						p["ws_path"] = "/entry"
						p["ws_host"] = "front.example.org"
						tr = map[string]any{"type": "ws", "path": "/entry"}
						alpn = []string{"http/1.1"}
					case "grpc":
						p["grpc_service_name"] = "test.Service"
						tr = map[string]any{"type": "grpc", "service_name": "test.Service"}
						alpn = []string{"h2"}
					case "tcp":
						if mode == "acme" {
							alpn = []string{"h2", "http/1.1"}
						}
					}
					cert := &agentproto.CertSpec{Mode: mode, Domain: "origin.example.org"}
					if mode == "external" {
						cert.ID = "fixture"
						p["cert_id"] = cert.ID
						f, err := EnsureSelfSigned(dir, cert.Domain)
						if err != nil {
							t.Fatal(err)
						}
						cert.CertPath, cert.KeyPath = f.Cert, f.Key
					}
					n := agentproto.NodeSpec{Core: "singbox", NodeID: 1, Protocol: protocol, ListenPort: 21001, Params: p, Cert: cert}
					nodes := []agentproto.NodeSpec{n}
					cfg, err := d.BuildConfig(ds, nodes)
					if err != nil {
						t.Fatal(err)
					}
					in := cfg["inbounds"].([]any)[0].(map[string]any)
					tls := in["tls"].(map[string]any)
					actualALPN, _ := tls["alpn"].([]string)
					if !reflect.DeepEqual(in["users"], []any{user}) || !reflect.DeepEqual(in["transport"], tr) || tls["reality"] != nil || tls["enabled"] != true || tls["server_name"] != cert.Domain || !reflect.DeepEqual(actualALPN, alpn) {
						t.Fatal("incorrect TLS/transport or legacy Vision fallback")
					}
					if mode == "acme" {
						if tls["certificate_provider"] == nil {
							t.Fatal("no ACME provider")
						}
					} else if tls["certificate_path"] == nil || tls["key_path"] == nil {
						t.Fatal("file certificate omitted")
					}
					// The complete config contains Linux-only routing marks. An optional
					// inbound-only check can run on developer hosts without pretending
					// to validate the Linux outbound, firewall, or service isolation.
					binary, check := os.Getenv("CTLVPS_OFFICIAL_CHECK_BIN"), cfg
					if binary == "" {
						binary = os.Getenv("CTLVPS_TLS_INBOUND_CHECK_BIN")
						check = map[string]any{"inbounds": []any{in}}
					}
					if binary != "" {
						body, err := json.Marshal(check)
						if err != nil {
							t.Fatal(err)
						}
						path := filepath.Join(dir, "check.json")
						if err := os.WriteFile(path, body, 0600); err != nil {
							t.Fatal(err)
						}
						if out, err := exec.Command(binary, "check", "-c", path).CombinedOutput(); err != nil {
							t.Fatalf("official config check: %v (%s)", err, out)
						}
					}
					ds.TLSTransportVersion = 0
					if _, err := d.BuildConfig(ds, nodes); err == nil {
						t.Fatal("legacy wire contract accepted")
					}
					ds.TLSTransportVersion = 1
					nodes[0].Network = &agentproto.NodeNetworkSpec{}
					if _, err := d.BuildConfig(ds, nodes); err == nil {
						t.Fatal("custom network silently dropped")
					}
					nodes[0].Network = nil
					if protocol == "vless" {
						p["flow"] = "xtls-rprx-vision"
						if _, err := d.BuildConfig(ds, nodes); err == nil {
							t.Fatal("Vision accepted for ordinary TLS")
						}
						delete(p, "flow")
					}
					p["reality_private_key"] = "fixture"
					if _, err := d.BuildConfig(ds, nodes); err == nil {
						t.Fatal("REALITY field ignored")
					}
				})
			}
		}
	}
}
