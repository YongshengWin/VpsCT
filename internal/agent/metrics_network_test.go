package agent

import (
	"ctlvps/internal/agentproto"
	"testing"
)

func TestLegacyCutoverUsesIdenticalCounterSample(t *testing.T) {
	s := &agentproto.NetworkSnapshot{Status: "ok", Interfaces: []agentproto.NetworkInterface{
		{Name: "eth0", CountersValid: true, Rx: 100, Tx: 200},
		{Name: "eth1", CountersValid: true, Rx: 30, Tx: 40},
		{Name: "wg0", CountersValid: true, Rx: 999, Tx: 999},
		{Name: "lo", CountersValid: true, Rx: 999, Tx: 999},
	}}
	for _, tc := range []struct {
		iface  string
		rx, tx int64
	}{{"eth0", 100, 200}, {"", 130, 240}, {"wg0", 0, 0}} {
		got, err := legacyNetworkBoundary(s, tc.iface, "boot")
		if err != nil || got.Rx != tc.rx || got.Tx != tc.tx {
			t.Fatal(tc, got, err)
		}
	}
	if _, err := legacyNetworkBoundary(s, "missing", "boot"); err == nil {
		t.Fatal("missing legacy interface accepted")
	}
	s.Status = "incomplete"
	if _, err := legacyNetworkBoundary(s, "", "boot"); err == nil {
		t.Fatal("partial aggregate accepted")
	}
	if _, err := legacyNetworkBoundary(s, "eth0", "boot"); err != nil {
		t.Fatal(err)
	}
	s.Interfaces[0].CountersValid = false
	if _, err := legacyNetworkBoundary(s, "eth0", "boot"); err == nil {
		t.Fatal("invalid counter accepted")
	}
}
