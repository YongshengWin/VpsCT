package core

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOfficialCertificateConfiguration(t *testing.T) {
	dir := t.TempDir()
	d := NewSingBox(Paths{DataDir: dir, LogDir: dir}, NewSystemd())
	n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: "trojan", ListenPort: 20443,
		Params: map[string]any{"password": "fixture-only"}, Cert: &agentproto.CertSpec{Mode: "acme", Domain: "example.com"}}
	for _, version := range []string{"1.12.14", "1.13.21", corecompat.NetworkBaseline} {
		ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: version}}}
		cfg, err := d.BuildConfig(ds, []agentproto.NodeSpec{n})
		if err != nil {
			t.Fatal(err)
		}
		tls := cfg["inbounds"].([]any)[0].(map[string]any)["tls"].(map[string]any)
		_, legacy := tls["acme"]
		_, modern := tls["certificate_provider"]
		if legacy == modern || modern != corecompat.ModernConfig(version) {
			t.Fatal("wrong TLS schema", version)
		}
		// Optional real-binary schema check; no ACME network operation is run.
		if bin := os.Getenv("CTLVPS_OFFICIAL_CHECK_BIN"); bin != "" && version == corecompat.NetworkBaseline {
			body, _ := json.Marshal(cfg)
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bin, "check", "-c", path).CombinedOutput(); err != nil {
				t.Fatalf("official check: %v\n%s", err, out)
			}
		}
	}
}
