package core

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/provision"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSSDeploymentConfig(t *testing.T) {
	dir := t.TempDir()
	d := NewSingBox(Paths{ConfDir: dir, DataDir: dir, CertDir: dir}, NewSystemd())
	for _, method := range []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"} {
		for _, network := range []string{"tcp", "both"} {
			n, err := provision.NewNode(domain.Server{ID: 1, PublicHost: "192.0.2.10"}, "", provision.Options{Protocol: "ss", Port: 30001, SSMethod: method, SSNetwork: network})
			if err != nil {
				t.Fatal(err)
			}
			var params map[string]any
			json.Unmarshal(n.ServerParams, &params)
			spec := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 30001, Params: params}
			cfg, err := d.BuildConfig(&agentproto.DesiredState{ServerID: 1}, []agentproto.NodeSpec{spec})
			if err != nil {
				t.Fatal(err)
			}
			in := cfg["inbounds"].([]any)[0].(map[string]any)
			if in["method"] != method || (network == "tcp" && in["network"] != "tcp") || (network == "both" && in["network"] != nil) {
				t.Fatal("wrong SS listener")
			}
			if bin, err := exec.LookPath("sing-box"); err == nil {
				// Darwin cannot initialize Linux routing marks; keep the inbound
				// unchanged and omit only that host-specific outbound option.
				if runtime.GOOS != "linux" {
					for _, out := range cfg["outbounds"].([]any) {
						delete(out.(map[string]any), "routing_mark")
					}
				}
				data, _ := json.Marshal(cfg)
				path := filepath.Join(dir, "ss.json")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command(bin, "check", "-c", path).CombinedOutput(); err != nil {
					t.Fatalf("%s/%s check: %v %s", method, network, err, out)
				}
			}
		}
	}
}
