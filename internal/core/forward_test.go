package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

func TestForwardCompilerSharesProcessWithoutNodeIdentityOrDNSFallback(t *testing.T) {
	f := agentproto.ForwardSpec{ForwardID: 1, Revision: 1, Config: networkconfig.Forward{
		ListenMode: "all", ListenPort: 24444, Network: "tcp", TargetHost: "203.0.113.10", TargetPort: 443,
		SourceMode: "cidr", MaxTCPConnections: 2,
	}, RuntimeNetwork: &networkconfig.Resolved{CollectorID: strings.Repeat("a", 32), BootID: "test", Sequence: 1, ListenAddress: "::"}}
	ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.12.14"}}}
	n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: "shadowsocks", ListenPort: 24443, Params: map[string]any{"method": "aes-128-gcm", "password": "fixture-only"}}
	d := &SingBox{}
	cfg, err := d.BuildResourceConfig(ds, []agentproto.NodeSpec{n}, []agentproto.ForwardSpec{f})
	if err != nil {
		t.Fatal(err)
	}
	in, out := cfg["inbounds"].([]any), cfg["outbounds"].([]any)
	if len(in) != 2 || len(out) != 2 {
		t.Fatal("missing shared resources")
	}
	forward := in[1].(map[string]any)
	if forward["tag"] != "forward-1" || forward["override_address"] != f.Config.TargetHost || forward["override_port"] != f.Config.TargetPort || out[1].(map[string]any)["routing_mark"] != uint32(0x45000001) || out[0].(map[string]any)["routing_mark"] != uint32(0x43000001) {
		t.Fatal("fixed destination or resource identity lost")
	}
	b, _ := json.Marshal(out[1])
	if strings.Contains(string(b), "resolver") || strings.Contains(string(b), "node-") {
		t.Fatal("forward inherited node/DNS behavior", string(b))
	}
	f.Config.ListenPort = n.ListenPort
	if _, err = d.BuildResourceConfig(ds, []agentproto.NodeSpec{n}, []agentproto.ForwardSpec{f}); err == nil {
		t.Fatal("cross-resource port collision accepted")
	}
	f.Config.ListenPort++
	for _, host := range []string{"example.com", "10.0.0.2", "169.254.169.254"} {
		f.Config.TargetHost = host
		if _, err = CompileForward(f, ds); err == nil {
			t.Fatal("unqualified target compiled", host)
		}
	}
	f.Config.TargetHost, f.RuntimeNetwork = "203.0.113.10", nil
	if _, err = CompileForward(f, ds); err == nil {
		t.Fatal("controller-only input compiled")
	}
	f.Blocked = true
	cfg, err = d.BuildResourceConfig(ds, []agentproto.NodeSpec{n}, []agentproto.ForwardSpec{f})
	if err != nil || len(cfg["inbounds"].([]any)) != 1 {
		t.Fatal("blocked forward started a listener", err)
	}
}

func TestForwardThroughSOCKS5KeepsForwardMarkAndPinnedUpstream(t *testing.T) {
	id := strings.Repeat("1", 32)
	snapshot := &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("a", 32), BootID: "fixture", Sequence: 1, SampledAt: time.Now(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: strings.Repeat("b", 32), Name: "wan1", Index: 2, Kind: "ether", Up: true, Carrier: true, Addresses: []string{"192.0.2.1/24"}, UsableAddresses: []string{"192.0.2.1/24"}}}}
	resolver := networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}
	upstream := networkconfig.SOCKS5{Server: "192.0.2.2", ServerPort: 1080, Authentication: "none", Family: "dual", DNS: resolver, Outer: networkconfig.Direct{InterfaceID: id, Family: "dual", DNS: resolver}, ConnectTimeoutSeconds: 10}
	f := agentproto.ForwardSpec{ForwardID: 1, Revision: 1, Config: networkconfig.Forward{ListenMode: "all", ListenPort: 24444, Network: "tcp", TargetHost: "203.0.113.10", TargetPort: 443, SourceMode: "all", MaxTCPConnections: 2, EgressProfileID: 3, EgressRevision: 1}, SOCKS5: &agentproto.SOCKS5Egress{Config: upstream}}
	resolved, err := netinventory.ResolveForwardTransport(f, snapshot, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.RuntimeNetwork = &resolved
	ds := &agentproto.DesiredState{Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.12.14"}}}
	compiled, err := (&SingBox{}).BuildResourceConfig(ds, nil, []agentproto.ForwardSpec{f})
	if err != nil {
		t.Fatal(err)
	}
	out := compiled["outbounds"].([]any)[0].(map[string]any)
	if out["type"] != "socks" || out["server"] != upstream.Server || out["routing_mark"] != uint32(0x45000001) {
		t.Fatal("forward lost its marked, pinned upstream", out)
	}
	rules := compiled["route"].(map[string]any)["rules"].([]any)
	if rules[len(rules)-1].(map[string]any)["outbound"] != "forward-1-socks5" {
		t.Fatal("forward escaped upstream")
	}
	plan := networkguard.Plan{Token: strings.Repeat("0", 32), Bindings: []networkguard.Binding{{ForwardID: f.ForwardID, Forward: &f.Config, ForwardTransport: true, ForwardFamily: upstream.Family, ListenPort: f.Config.ListenPort, Core: "singbox", Wanted: f.Config.BindingPolicy(), Applied: resolved}}}
	guard, err := plan.Rules()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(guard, "ip daddr 192.0.2.2 tcp dport 1080 return") || strings.Contains(guard, "ip daddr 203.0.113.10 tcp dport 443") {
		t.Fatal("guard did not isolate upstream from business target")
	}
	f.RuntimeNetwork.SOCKS5.Address = "192.0.2.3"
	if _, err = CompileForward(f, ds); err == nil {
		t.Fatal("changed upstream bypassed compiler pin")
	}
}
