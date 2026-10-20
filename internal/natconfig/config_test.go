package natconfig

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeDefaultsAndRange(t *testing.T) {
	c, err := Decode([]byte(`{"schema":1,"interface":"eth0.100","local_address":"192.0.2.10","mappings":[{"public_port":30003,"local_port":10003},{"public_port":30001,"local_port":10001,"count":2,"network":"tcp"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Mappings[0].Count != 1 || c.Mappings[0].Network != "both" {
		t.Fatalf("defaults not normalized: %+v", c.Mappings[0])
	}
	ports, err := c.Expand()
	if err != nil {
		t.Fatal(err)
	}
	want := []PortMapping{{30001, 10001, "tcp"}, {30002, 10002, "tcp"}, {30003, 10003, "tcp"}, {30003, 10003, "udp"}}
	if !reflect.DeepEqual(ports, want) {
		t.Fatalf("expanded mapping: %+v; want %+v", ports, want)
	}
}

func TestTransportPortOwnership(t *testing.T) {
	base := Config{Schema: 1, Mappings: []Mapping{{PublicPort: 30001, LocalPort: 10001, Network: "tcp"}, {PublicPort: 30001, LocalPort: 10002, Network: "udp"}}}
	if err := base.Validate(); err != nil {
		t.Fatalf("separate TCP/UDP public mappings rejected: %v", err)
	}
	base.Mappings[1].PublicPort, base.Mappings[1].LocalPort = 30002, 10001
	if err := base.Validate(); err != nil {
		t.Fatalf("separate TCP/UDP local mappings rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		maps []Mapping
	}{
		{"public overlap", []Mapping{{PublicPort: 30000, LocalPort: 10000, Count: 3, Network: "tcp"}, {PublicPort: 30002, LocalPort: 11000, Network: "tcp"}}},
		{"local overlap", []Mapping{{PublicPort: 30000, LocalPort: 10000, Count: 3, Network: "udp"}, {PublicPort: 31000, LocalPort: 10002, Network: "udp"}}},
		{"both overlaps tcp", []Mapping{{PublicPort: 30000, LocalPort: 10000}, {PublicPort: 30000, LocalPort: 10001, Network: "tcp"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := (Config{Schema: 1, Mappings: tc.maps}).Validate(); err == nil {
				t.Fatal("accepted duplicate transport port")
			}
		})
	}
}

func TestPrivilegeAndPortBoundaries(t *testing.T) {
	for _, m := range []Mapping{{PublicPort: 22, LocalPort: 22}, {PublicPort: 1, LocalPort: 1, Count: 1024}, {PublicPort: 65535, LocalPort: 65535}, {PublicPort: 443, LocalPort: 10443}, {PublicPort: 30000, LocalPort: 1024}} {
		if err := (Config{Schema: 1, Mappings: []Mapping{m}}).Validate(); err != nil {
			t.Errorf("valid boundary %+v: %v", m, err)
		}
	}
	for _, m := range []Mapping{{PublicPort: 30000, LocalPort: 22}, {PublicPort: 30000, LocalPort: 1023}, {PublicPort: 0, LocalPort: 10000}, {PublicPort: 30000, LocalPort: 65536}, {PublicPort: 65535, LocalPort: 10000, Count: 2}, {PublicPort: 30000, LocalPort: 65535, Count: 2}, {PublicPort: 30000, LocalPort: 10000, Count: -1}, {PublicPort: 30000, LocalPort: 10000, Count: math.MaxInt}} {
		if err := (Config{Schema: 1, Mappings: []Mapping{m}}).Validate(); err == nil {
			t.Errorf("accepted invalid mapping %+v", m)
		}
	}
}

func TestExpansionBudget(t *testing.T) {
	c := Config{Schema: 1, Mappings: []Mapping{{PublicPort: 30000, LocalPort: 10000, Count: MaxPortPairs}}}
	ports, err := c.Expand()
	if err != nil || len(ports) != 2*MaxPortPairs {
		t.Fatalf("maximum budget not accepted: count=%d err=%v", len(ports), err)
	}
	c.Mappings = append(c.Mappings, Mapping{PublicPort: 50000, LocalPort: 20000})
	if err := c.Validate(); err == nil {
		t.Fatal("accepted oversized expanded range")
	}
	c.Mappings = make([]Mapping, MaxPortPairs+1)
	if err := c.Validate(); err == nil {
		t.Fatal("accepted oversized mapping collection")
	}
}

func TestDecodeRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, b := range []string{
		`{"schema":1,"mappings":[],"command":"ignored"}`,
		`{"schema":1,"mappings":[{"public_port":30001,"local_port":10001,"listen_port":30001}]}`,
		`{"schema":1,"mappings":[]} {"schema":1}`,
		`{"schema":1,"mappings":[]} garbage`,
		`{"schema":1,"mappings":[`,
		`null`,
		`{"mappings":[]}`,
		`{"schema":2,"mappings":[]}`,
		`{"schema":1,"mappings":[{"public_port":30001,"local_port":10001,"network":"TCP"}]}`,
		`{"schema":1,"mappings":[{"public_port":"30001","local_port":10001}]}`,
	} {
		if _, err := Decode([]byte(b)); err == nil {
			t.Errorf("accepted invalid JSON %s", b)
		}
	}
	c, err := Decode([]byte("{\"schema\":1,\"mappings\":[]} \n\t"))
	if err != nil {
		t.Fatal(err)
	}
	ports, err := c.Expand()
	if err != nil || len(ports) != 0 {
		t.Fatalf("empty opt-in should be disabled: %+v %v", ports, err)
	}
}

func TestAddressAndInterfaceScope(t *testing.T) {
	for _, name := range []string{"eth0", "enp1s0.100", "veth-peer_1", strings.Repeat("a", 15)} {
		if err := (Config{Schema: 1, Interface: name}).Validate(); err != nil {
			t.Errorf("valid interface %q: %v", name, err)
		}
	}
	for _, name := range []string{".", "..", "eth0:1", "eth0/peer", "eth0\n", `eth0"`, "eth*", strings.Repeat("a", 16)} {
		if err := (Config{Schema: 1, Interface: name}).Validate(); err == nil {
			t.Errorf("accepted invalid interface %q", name)
		}
	}
	for _, addr := range []string{"::1", "::ffff:192.0.2.10", "192.0.2.10/24", "0.0.0.0", "127.0.0.1", "224.0.0.1", "255.255.255.255", "example.test"} {
		if err := (Config{Schema: 1, LocalAddress: addr}).Validate(); err == nil {
			t.Errorf("accepted invalid address %q", addr)
		}
	}
}

func TestExpansionStableAcrossMappingOrder(t *testing.T) {
	maps := []Mapping{{PublicPort: 31000, LocalPort: 11000, Network: "udp"}, {PublicPort: 30000, LocalPort: 10000, Count: 2}}
	a, err := (Config{Schema: 1, Mappings: maps}).Expand()
	if err != nil {
		t.Fatal(err)
	}
	maps[0], maps[1] = maps[1], maps[0]
	b, err := (Config{Schema: 1, Mappings: maps}).Expand()
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("order changed expansion: %+v %+v %v", a, b, err)
	}
	encoded, err := json.Marshal(Config{Schema: 1, Mappings: maps})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := decoded.Expand()
	if err != nil || !reflect.DeepEqual(a, roundtrip) {
		t.Fatalf("JSON round-trip changed mapping: %+v %v", roundtrip, err)
	}
}
