package provision

import (
	"ctlvps/internal/natconfig"
	"testing"
)

func TestNATAllocationUsesProtocolPoolAndReservations(t *testing.T) {
	c := &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001, Network: "tcp"}, {PublicPort: 30002, LocalPort: 10002, Network: "udp"}, {PublicPort: 30003, LocalPort: 10003, Network: "both"}}}
	for _, in := range []struct {
		p      string
		params map[string]any
		want   int
	}{{"ss", nil, 30003}, {"ss", map[string]any{"network": "tcp"}, 30001}, {"vless", nil, 30001}, {"hysteria2", nil, 30002}, {"mieru", nil, 30001}, {"mieru", map[string]any{"transport": "UDP"}, 30002}} {
		used := map[int]bool{}
		if in.want != 30003 {
			used[30003] = true
		}
		got, err := AllocateNodePort(used, c, in.p, in.params)
		if err != nil || got != in.want {
			t.Fatal(in.p, got, err)
		}
	}
	if _, err := AllocateNodePort(map[int]bool{30003: true}, c, "ss", nil); err == nil {
		t.Fatal("SS allocated one-transport port")
	}
	c.Mappings = append(c.Mappings, natconfig.Mapping{PublicPort: 30004, LocalPort: 30003, Network: "tcp"})
	if _, err := AllocateNodePort(map[int]bool{}, c, "ss", nil); err == nil {
		t.Fatal("allocated another mapping's local source")
	}
	c = &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30005, LocalPort: 30005, Network: "both"}}}
	if p, err := AllocateNodePort(nil, c, "ss", nil); err != nil || p != 30005 {
		t.Fatal("identity mapping", p, err)
	}
}
