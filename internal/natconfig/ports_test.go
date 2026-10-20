package natconfig

import (
	"reflect"
	"testing"
)

func TestProtocolTransportRequirements(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		params   map[string]any
		want     []string
	}{
		{"ss", nil, []string{"tcp", "udp"}}, {"ss", map[string]any{"network": "tcp"}, []string{"tcp"}}, {"shadowsocks", nil, []string{"tcp", "udp"}},
		{"hysteria2", nil, []string{"udp"}}, {"tuic", nil, []string{"udp"}}, {"wireguard", nil, []string{"udp"}},
		{"vless", nil, []string{"tcp"}}, {"vmess", nil, []string{"tcp"}}, {"trojan", nil, []string{"tcp"}},
		{"anytls", nil, []string{"tcp"}}, {"snell", nil, []string{"tcp"}},
		{"mieru", map[string]any{"transport": "TCP"}, []string{"tcp"}},
		{"mieru", map[string]any{"transport": "UDP"}, []string{"udp"}},
	} {
		got, err := ProtocolNetworks(tc.protocol, tc.params)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s requirements=%v err=%v", tc.protocol, got, err)
		}
	}
	for _, tc := range []struct {
		protocol string
		params   map[string]any
	}{
		{"ss", map[string]any{"network": "udp"}}, {"ss", map[string]any{"network": []any{}}}, {"unknown", nil}, {"mieru", nil}, {"mieru", map[string]any{"transport": "tcp"}},
	} {
		if _, err := ProtocolNetworks(tc.protocol, tc.params); err == nil {
			t.Fatal("unknown transport guessed:", tc)
		}
	}
}

func TestPoolsRequireAllTransportsAndKeepIdentityPorts(t *testing.T) {
	c := Config{Schema: 1, Mappings: []Mapping{
		{PublicPort: 30004, LocalPort: 30004, Network: "udp"},
		{PublicPort: 30001, LocalPort: 10001, Count: 2, Network: "tcp"},
		{PublicPort: 30002, LocalPort: 10004, Count: 2, Network: "udp"},
	}}
	pools, err := c.Pools()
	want := map[string][]int{"tcp": {30001, 30002}, "udp": {30002, 30003, 30004}, "both": {30002}}
	if err != nil || !reflect.DeepEqual(pools, want) {
		t.Fatalf("pools=%v err=%v", pools, err)
	}
	for _, tc := range []struct {
		port     int
		networks []string
		want     bool
	}{
		{30001, []string{"tcp"}, true}, {30001, []string{"tcp", "udp"}, false},
		{30002, []string{"tcp", "udp"}, true}, {30004, []string{"udp"}, true},
		{30004, []string{"tcp"}, false}, {30005, []string{"udp"}, false},
	} {
		got, err := c.SupportsPort(tc.port, tc.networks)
		if err != nil || got != tc.want {
			t.Fatalf("port=%d networks=%v supported=%v err=%v", tc.port, tc.networks, got, err)
		}
	}
	for _, networks := range [][]string{nil, {"both"}, {"raw"}} {
		if _, err := c.SupportsPort(30002, networks); err == nil {
			t.Fatal("invalid transport requirement accepted:", networks)
		}
	}
	c.Mappings = append(c.Mappings, Mapping{PublicPort: 30002, LocalPort: 40001, Network: "tcp"})
	if _, err := c.Pools(); err == nil {
		t.Fatal("invalid config generated a usable pool")
	}
}
