package networkconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestForwardDefaultsDenySourcesAndRejectUnboundedOrAmbiguousInput(t *testing.T) {
	f := Forward{ListenMode: "all", ListenPort: 24443, Network: "tcp", TargetHost: "Origin.Example.Test.", TargetPort: 443, SourceMode: "cidr", MaxTCPConnections: 32}
	raw, _ := json.Marshal(f)
	got, err := DecodeForward(raw)
	if err != nil || got.TargetHost != "origin.example.test" || got.SourceMode != "cidr" || len(got.SourceCIDRs) != 0 {
		t.Fatal(got, err)
	}
	for _, modify := range []func(*Forward){
		func(f *Forward) { f.SourceMode = "" },
		func(f *Forward) { f.SourceCIDRs = []string{"0.0.0.0/0"} },
		func(f *Forward) { f.SourceCIDRs = []string{"192.0.2.1/24"} },
		func(f *Forward) { f.SourceCIDRs = []string{"::ffff:192.0.2.0/120"} },
		func(f *Forward) { f.SourceCIDRs = []string{"192.0.2.0/24", "192.0.2.0/24"} },
		func(f *Forward) { f.TargetHost = "https://example.test:443/path" },
		func(f *Forward) { f.MaxTCPConnections = 0 },
		func(f *Forward) { f.EgressProfileID = 1 },
		func(f *Forward) { f.Network = "both" },
	} {
		bad := f
		modify(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("invalid forward accepted", bad)
		}
	}
	if _, err := DecodeForward([]byte(strings.TrimSuffix(string(raw), "}") + `,"share_id":1}`)); err == nil {
		t.Fatal("forward accepted share ownership")
	}
	f.SourceMode = "all"
	if err := f.Validate(); err != nil {
		t.Fatal("explicit public listener rejected", err)
	}
}
