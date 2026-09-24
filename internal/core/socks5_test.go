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

func TestSOCKS5CompilationKeepsDNSAndConsumerIdentitiesSeparate(t *testing.T) {
	id := strings.Repeat("1", 32)
	iface := networkconfig.Interface{ID: id, Name: "wan1", Index: 5}
	cfg := networkconfig.SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "password", UDP: true, Family: "ipv6", DNS: networkconfig.Resolver{Transport: "udp", Address: "2001:db8::53", Port: 53},
		Outer: networkconfig.Direct{InterfaceID: id, Family: "ipv4", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}}, ConnectTimeoutSeconds: 10}
	outer := networkconfig.ResolvedDirect{Config: cfg.Outer, Interface: &iface, Owners: []networkconfig.Interface{iface}}
	secret := networkconfig.SOCKS5Credentials{Username: "fixture", Password: "private-fixture"}
	a, err := CompileSOCKS5(1, cfg, secret, outer)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CompileSOCKS5(2, cfg, secret, outer)
	if err != nil {
		t.Fatal(err)
	}
	if a.Outbound["routing_mark"] == b.Outbound["routing_mark"] || a.Outbound["tag"] == b.Outbound["tag"] || a.DNS["tag"] == b.DNS["tag"] || a.BootstrapDNS["tag"] == b.BootstrapDNS["tag"] {
		t.Fatal("different consumers share sockets or resolver identity")
	}
	if a.DNS["detour"] != a.Outbound["tag"] || a.BootstrapDNS["detour"] != nil || a.DNS["bind_interface"] != nil || a.BootstrapDNS["bind_interface"] != "wan1" || a.FamilyReject["ip_version"] != 4 {
		t.Fatal("business DNS escaped or outer family overwrote business family")
	}
	wire, _ := json.Marshal([]any{a.DNS, a.BootstrapDNS, a.ResolveRule, a.FamilyReject})
	if strings.Contains(string(wire), "private-fixture") {
		t.Fatal("credential copied outside its outbound")
	}
	outer.Config.Family = "dual"
	if _, err := CompileSOCKS5(1, cfg, secret, outer); err == nil {
		t.Fatal("mismatched local resolution accepted")
	}
}

func TestBuildConfigRoutesBusinessAndDNSIntoGuardedSOCKS5(t *testing.T) {
	id := strings.Repeat("1", 32)
	snapshot := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "fixture", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: strings.Repeat("b", 32), Name: "wan1", Index: 2, Kind: "ether", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}
	resolver := networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}
	cfg := networkconfig.SOCKS5{Server: "192.0.2.2", ServerPort: 1080, Authentication: "none", Family: "dual", DNS: resolver, Outer: networkconfig.Direct{InterfaceID: id, Family: "dual", DNS: resolver}, ConnectTimeoutSeconds: 10}
	policy := networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1}
	r, err := netinventory.ResolveSOCKS5(policy, cfg, snapshot, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 21001, Network: &agentproto.NodeNetworkSpec{Policy: policy, SOCKS5: &agentproto.SOCKS5Egress{Config: cfg}}, RuntimeNetwork: &r}
	ds := &agentproto.DesiredState{IPv4Only: true, Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.12.14"}}}
	driver := &SingBox{}
	compiled, err := driver.BuildConfig(ds, []agentproto.NodeSpec{n})
	if err != nil {
		t.Fatal(err)
	}
	outs := compiled["outbounds"].([]any)
	out := outs[0].(map[string]any)
	if len(outs) != 1 || out["type"] != "socks" || out["server"] != "192.0.2.2" || out["bind_interface"] != "wan1" || out["routing_mark"] != uint32(0x43000001) {
		t.Fatal("production compiler lost guarded transport")
	}
	dns := compiled["dns"].(map[string]any)["servers"].([]any)
	if dns[len(dns)-1].(map[string]any)["detour"] != "node-1-socks5" {
		t.Fatal("business DNS escaped SOCKS")
	}
	rules := compiled["route"].(map[string]any)["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["outbound"] != "node-1-socks5" {
		t.Fatal("business route still points at direct")
	}
	var udpReject, ipv6Reject, resolved, rechecked bool
	for _, raw := range rules {
		rule := raw.(map[string]any)
		udpReject = udpReject || rule["network"] == "udp" && rule["action"] == "reject"
		ipv6Reject = ipv6Reject || rule["ip_version"] == 6 && rule["action"] == "reject"
		if resolved && rule["ip_is_private"] == true && rule["action"] == "reject" {
			rechecked = true
		}
		resolved = resolved || rule["action"] == "resolve"
	}
	if !udpReject || !ipv6Reject || !rechecked {
		t.Fatal("TCP-only, IPv4-only or post-DNS private protection lost")
	}
	n.Protocol = "vless"
	if _, err = driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
		t.Fatal("unverified independent handshake path inherited SOCKS admission")
	}
	n.Protocol = "ss"
	n.Network.SOCKS5.Config.Server = "Upstream.Example."
	r.SOCKS5.Host = "upstream.example"
	r.SOCKS5.SetDNSLifetime(time.Now(), 60)
	compiled, err = driver.BuildConfig(ds, []agentproto.NodeSpec{n})
	if err != nil {
		t.Fatal("validated domain pin rejected", err)
	}
	if compiled["outbounds"].([]any)[0].(map[string]any)["server"] != "192.0.2.2" {
		t.Fatal("core was allowed to resolve the upstream a second time")
	}
	r.SOCKS5.Host = "other.example"
	if _, err = driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
		t.Fatal("pin from a different domain accepted")
	}
	n.Network.SOCKS5.Config.Server = cfg.Server
	r.SOCKS5.Host = ""
	*r.SOCKS5 = r.SOCKS5.WithoutDNSLifetime()
	r.SOCKS5.Address = "192.0.2.3"
	if _, err = driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
		t.Fatal("compiler accepted a different firewall endpoint")
	}
	r.SOCKS5 = nil
	if _, err = driver.BuildConfig(ds, []agentproto.NodeSpec{n}); err == nil {
		t.Fatal("compiler accepted unguarded SOCKS")
	}
}
