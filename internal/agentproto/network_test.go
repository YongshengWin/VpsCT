package agentproto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNetworkValidationAndLegacyWireShape(t *testing.T) {
	b, err := json.Marshal(Heartbeat{Metrics: Metrics{Interface: "eth0"}})
	if err != nil || strings.Contains(string(b), `"network"`) {
		t.Fatal("legacy metrics gained network field")
	}
	valid := func() *NetworkSnapshot {
		return &NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "boot", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []NetworkInterface{{ID: strings.Repeat("b", 32), Generation: strings.Repeat("c", 32), Name: "eth0", Index: 2, Kind: "physical", CountersValid: true}}}
	}
	if err := ValidateNetwork(valid()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*NetworkSnapshot){
		func(s *NetworkSnapshot) { s.Interfaces = append(s.Interfaces, s.Interfaces[0]) },
		func(s *NetworkSnapshot) { s.Interfaces[0].Rx = -1 },
		func(s *NetworkSnapshot) { s.Interfaces[0].Addresses = []string{"not-an-ip"} },
		func(s *NetworkSnapshot) { s.Interfaces[0].CountersValid = false; s.Interfaces[0].Rx = 1 },
		func(s *NetworkSnapshot) { s.Status = "error" },
		func(s *NetworkSnapshot) { s.Sequence = 0 },
	} {
		s := valid()
		change(s)
		if ValidateNetwork(s) == nil {
			t.Fatal("accepted invalid network")
		}
	}
}
