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

func TestOfficialCertificateConfiguration(t *testing.T) {
	for _, version := range []string{"1.12.14", "1.13.21", corecompat.NetworkBaseline} {
		for _, protocol := range []string{"anytls", "trojan", "hysteria2", "tuic"} {
			for _, mode := range []string{"acme", "self_signed", "external"} {
				t.Run(version+"/"+protocol+"/"+mode, func(t *testing.T) {
					dir := t.TempDir()
					d := NewSingBox(Paths{DataDir: dir, LogDir: dir}, NewSystemd())
					d.CertResolver = func(*agentproto.CertSpec) (CertFiles, error) {
						return CertFiles{Cert: filepath.Join(dir, "cert.pem"), Key: filepath.Join(dir, "key.pem")}, nil
					}
					n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: protocol, ListenPort: 20443,
						Params: map[string]any{"password": "fixture-only", "uuid": "11111111-1111-4111-8111-111111111111"}, Cert: &agentproto.CertSpec{Mode: mode, Domain: "example.com"}}
					ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: version}}}
					cfg, err := d.BuildConfig(ds, []agentproto.NodeSpec{n})
					if err != nil {
						t.Fatal(err)
					}
					tls := cfg["inbounds"].([]any)[0].(map[string]any)["tls"].(map[string]any)
					_, legacy := tls["acme"]
					_, modern := tls["certificate_provider"]
					if mode == "acme" {
						if legacy == modern || modern != corecompat.ModernConfig(version) {
							t.Fatal("wrong ACME schema")
						}
					} else if legacy || modern || tls["certificate_path"] == nil || tls["key_path"] == nil {
						t.Fatal("file certificate configuration changed")
					}
					var wantALPN []string
					if protocol == "hysteria2" || protocol == "tuic" {
						wantALPN = []string{"h3"}
					} else if mode == "acme" {
						wantALPN = []string{"h2", "http/1.1"}
					}
					gotALPN, _ := tls["alpn"].([]string)
					if !reflect.DeepEqual(gotALPN, wantALPN) {
						t.Fatalf("ALPN = %v, want %v", gotALPN, wantALPN)
					}
					// Optional official-binary schema check; certificate issuance is
					// exercised separately in the isolated ACME integration suite.
					if bin := os.Getenv("CTLVPS_OFFICIAL_CHECK_BIN"); bin != "" && version == corecompat.NetworkBaseline && mode == "acme" {
						body, err := json.Marshal(cfg)
						if err != nil {
							t.Fatal(err)
						}
						path := filepath.Join(dir, "config.json")
						if err := os.WriteFile(path, body, 0600); err != nil {
							t.Fatal(err)
						}
						if out, err := exec.Command(bin, "check", "-c", path).CombinedOutput(); err != nil {
							t.Fatalf("official check: %v\n%s", err, out)
						}
					}
				})
			}
		}
	}
}

func TestACMEPreservesExplicitApplicationProtocols(t *testing.T) {
	d := NewSingBox(Paths{DataDir: t.TempDir()}, NewSystemd())
	for _, protocol := range []string{"anytls", "trojan"} {
		for _, version := range []string{"1.12.14", corecompat.NetworkBaseline} {
			t.Run(protocol+"/"+version, func(t *testing.T) {
				n := agentproto.NodeSpec{Protocol: protocol, Cert: &agentproto.CertSpec{Mode: "acme", Domain: "example.com"}}
				want := []string{"http/1.1"}
				block, err := d.tlsBlock(n, &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: version}}}, want)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(block["alpn"], want) {
					t.Fatal("explicit ALPN replaced by defaults:", block["alpn"])
				}
			})
		}
	}
}
