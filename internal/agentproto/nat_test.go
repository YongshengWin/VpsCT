package agentproto

import (
	"encoding/json"
	"strings"
	"testing"

	"ctlvps/internal/natconfig"
)

func TestNATDesiredVersionAndHash(t *testing.T) {
	d := &DesiredState{ServerID: 1, Revision: 1}
	d.Hash = ContentHash(d)
	legacy, _ := json.Marshal(d)
	if strings.Contains(string(legacy), "nat") {
		t.Fatal("legacy desired payload changed")
	}
	d.NAT = &natconfig.Config{Schema: 1}
	d.Hash = ContentHash(d)
	if err := ValidateDesired(d, 1, 0, ""); err == nil {
		t.Fatal("controller opt-out accepted without v2")
	}
	d.NATVersion = NATVersion
	d.Hash = ContentHash(d)
	if err := ValidateDesired(d, 1, 0, ""); err != nil {
		t.Fatal("explicit controller opt-out rejected:", err)
	}
	d.NAT.Mappings = []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001}}
	if err := ValidateDesired(d, 1, 0, ""); err == nil {
		t.Fatal("NAT change did not invalidate desired hash")
	}
	d.Hash = ContentHash(d)
	if err := ValidateDesired(d, 1, 0, ""); err != nil {
		t.Fatal(err)
	}
	d.NAT.Mappings[0].LocalPort = 22
	d.Hash = ContentHash(d)
	if err := ValidateDesired(d, 1, 0, ""); err == nil {
		t.Fatal("unsafe controller mapping accepted")
	}
	d.NAT, d.NATVersion = nil, 3
	d.Hash = ContentHash(d)
	if err := ValidateDesired(d, 1, 0, ""); err == nil {
		t.Fatal("future NAT contract accepted")
	}
}

func TestNATStatusDoesNotConfuseCapabilityWithApplication(t *testing.T) {
	empty := &natconfig.Config{Schema: 1}
	enabled := &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001}}}
	for _, s := range []*NATStatus{
		nil,
		{Version: 2, Source: "none", Status: "disabled"},
		{Version: 2, Source: "local", Status: "disabled", Config: empty},
		{Version: 2, Source: "controller", Status: "pending", Configured: true, Config: enabled},
		{Version: 2, Source: "controller", Status: "applied", Configured: true, Config: enabled},
		{Version: 2, Source: "local", Status: "error", Error: "root file unreadable"},
	} {
		if err := ValidateNAT(s); err != nil {
			t.Fatalf("valid snapshot rejected: %+v: %v", s, err)
		}
	}
	for _, s := range []*NATStatus{
		{Version: 1, Source: "none", Status: "disabled"},
		{Version: 2, Source: "remote", Status: "disabled"},
		{Version: 2, Source: "none", Status: "applied"},
		{Version: 2, Source: "none", Status: "pending", Config: empty},
		{Version: 2, Source: "local", Status: "applied", Configured: true},
		{Version: 2, Source: "local", Status: "disabled", Config: enabled},
		{Version: 2, Source: "local", Status: "error"},
		{Version: 2, Source: "local", Status: "pending"},
		{Version: 2, Source: "local", Status: "error", Error: strings.Repeat("x", 513)},
		{Version: 2, Source: "local", Status: "error", Error: string([]byte{0xff})},
	} {
		if err := ValidateNAT(s); err == nil {
			t.Fatalf("inconsistent snapshot accepted: %+v", s)
		}
	}
}
