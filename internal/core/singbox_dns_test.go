package core

import (
	"testing"

	"ctlvps/internal/agentproto"
)

func TestLocalDNSCompatibility(t *testing.T) {
	for _, version := range []string{"1.12.14", "1.13.21", "1.14.1"} {
		t.Run(version, func(t *testing.T) {
			d := NewSingBox(Paths{}, NewSystemd())
			ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: version}}}
			cfg, err := d.BuildConfig(ds, nil)
			if err != nil {
				t.Fatal(err)
			}
			local := cfg["dns"].(map[string]any)["servers"].([]any)[0].(map[string]any)
			if version == "1.12.14" {
				if _, exists := local["prefer_go"]; exists {
					t.Fatal("1.12 rejects prefer_go; legacy configuration must omit it")
				}
			} else if local["prefer_go"] != true {
				t.Fatal("local DNS must work without systemd-resolved on 1.13+")
			}
		})
	}
}
