package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
)

func TestCompileDirectPreservesAccountingAndRequiresResolvedOwnership(t *testing.T) {
	id := "11111111111111111111111111111111"
	owner := networkconfig.Interface{ID: id, Name: "wan1", Index: 2}
	d := networkconfig.ResolvedDirect{Config: networkconfig.Direct{InterfaceID: id, Family: "ipv4", SourceIPv4: &networkconfig.Address{InterfaceID: id, Address: "192.0.2.1"}, DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}, Interface: &owner, Owners: []networkconfig.Interface{owner}}
	c, err := CompileDirect(257, d)
	if err != nil {
		t.Fatal(err)
	}
	for name, fields := range map[string]map[string]any{"outbound": c.Outbound, "DNS": c.DNS, "handshake": c.Dial} {
		if fields["routing_mark"] != uint32(0x43000101) || fields["bind_interface"] != "wan1" || fields["inet4_bind_address"] != "192.0.2.1" {
			t.Fatalf("%s lost binding/accounting: %+v", name, fields)
		}
	}
	if _, ok := c.DNS["domain_resolver"]; ok {
		t.Fatal("DNS endpoint must not recursively resolve itself")
	}
	if c.FamilyReject == nil || c.ResolveRule["strategy"] != "ipv4_only" {
		t.Fatal("address-family policy lost")
	}
	forward, err := CompileResourceDirect(agentproto.ResourceIdentity{Kind: "forward", ID: 257}, d)
	if err != nil || forward.Outbound["routing_mark"] != uint32(0x45000101) || forward.Outbound["tag"] != "forward-257-direct" || forward.DNS["tag"] != "forward-257-dns" {
		t.Fatal("forward reused node identity", forward, err)
	}
	d.Interface = nil
	if _, err = CompileDirect(257, d); err == nil {
		t.Fatal("unresolved interface accepted")
	}
	d.Config.InterfaceID = ""
	c, err = CompileDirect(257, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, bound := c.Outbound["bind_interface"]; bound {
		t.Fatal("source-only binding unexpectedly changed routing")
	}
	d.Owners = nil
	if _, err = CompileDirect(257, d); err == nil {
		t.Fatal("unresolved source owner accepted")
	}
}

func TestBuildConfigWiresLocalBindingAndRealityDial(t *testing.T) {
	id := strings.Repeat("1", 32)
	s := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "test", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: strings.Repeat("b", 32), Name: "wan1", Index: 2, Kind: "ether", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}
	n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: "vless", ListenPort: 24443, Params: map[string]any{"handshake_server": "example.test"}, Network: &agentproto.NodeNetworkSpec{Policy: networkconfig.Node{ListenMode: "address", ListenInterfaceID: id, ListenAddress: "192.0.2.1", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}, Direct: &networkconfig.Direct{InterfaceID: id, Family: "dual", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}}}
	ds := &agentproto.DesiredState{IPv4Only: true, Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.12.14"}}}
	driver := &SingBox{}
	if _, err := driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
		t.Fatal("compiler accepted unresolved remote policy")
	}
	resolved, err := netinventory.ResolveBinding(n.Network.Policy, n.Network.Direct, s, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	n.RuntimeNetwork = &resolved
	cfg, err := driver.BuildConfig(ds, []agentproto.NodeSpec{n})
	if err != nil {
		t.Fatal(err)
	}
	in := cfg["inbounds"].([]any)[0].(map[string]any)
	if in["listen"] != "192.0.2.1" {
		t.Fatal("listener address lost")
	}
	dial := in["tls"].(map[string]any)["reality"].(map[string]any)["handshake"].(map[string]any)
	if dial["bind_interface"] != "wan1" || dial["routing_mark"] != uint32(0x43000001) || dial["domain_resolver"] == nil {
		t.Fatal("Reality escaped its node outbound policy", dial)
	}
	dns := cfg["dns"].(map[string]any)
	if dns["independent_cache"] != true || len(dns["servers"].([]any)) != 2 {
		t.Fatal("missing isolated node resolver")
	}
	rules := cfg["route"].(map[string]any)["rules"].([]any)
	if len(rules) != 5 || rules[1].(map[string]any)["action"] != "resolve" || rules[2].(map[string]any)["ip_version"] != 6 || rules[3].(map[string]any)["ip_is_private"] != true {
		t.Fatal("resolved/private/family route order changed", rules)
	}
	// A local observation must correspond to the exact desired direct config.
	n.RuntimeNetwork.Direct.Config.DNS.Port++
	if _, err = driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
		t.Fatal("mismatched local candidate accepted")
	}
	n.Network, n.RuntimeNetwork = nil, nil
	legacy, err := driver.BuildConfig(ds, []agentproto.NodeSpec{n})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(legacy)
	if strings.Contains(string(b), "independent_cache") || strings.Contains(string(b), "bind_interface") || legacy["inbounds"].([]any)[0].(map[string]any)["listen"] != "::" {
		t.Fatal("legacy defaults changed")
	}
}
