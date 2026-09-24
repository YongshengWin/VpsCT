package agentproto

import (
	"encoding/json"
	"strings"
	"testing"

	"ctlvps/internal/networkconfig"
)

func TestNodeNetworkWireBoundary(t *testing.T) {
	n := NodeSpec{NodeID: 1, Core: "singbox"}
	legacy, _ := json.Marshal(n)
	if strings.Contains(string(legacy), "network") {
		t.Fatal("legacy wire representation changed")
	}
	n.Network = &NodeNetworkSpec{Policy: networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}}
	d := &DesiredState{Nodes: []NodeSpec{n}}
	before := ContentHash(d)
	d.Nodes[0].RuntimeNetwork = &networkconfig.Resolved{ListenAddress: "192.0.2.1"}
	if ContentHash(d) != before {
		t.Fatal("local observations changed remote content hash")
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "192.0.2.1") {
		t.Fatal("runtime observation escaped onto the wire")
	}
	var injected NodeSpec
	if err := json.Unmarshal([]byte(`{"runtime_network":{"ListenAddress":"192.0.2.1"},"RuntimeNetwork":{}}`), &injected); err != nil || injected.RuntimeNetwork != nil {
		t.Fatal("remote runtime observation accepted", err)
	}
	d.Nodes[0].Network.Policy.AdvertiseMode = "override"
	if ContentHash(d) == before {
		t.Fatal("network policy omitted from content hash")
	}
	d.Nodes[0].Network.Policy.EgressProfileID = 1
	d.Nodes[0].Network.Policy.EgressRevision = 1
	if d.Nodes[0].Network.Validate() == nil {
		t.Fatal("unresolved profile reference accepted")
	}
	for _, core := range []string{"singbox", "snell", "mita"} {
		for _, version := range []string{"1.11.0", "1.12.14", "1.15.0", ""} {
			if NetworkBindingSupported(core, version) != (core == "singbox" && version == "1.12.14") {
				t.Fatal("unverified capability", core, version)
			}
		}
	}
}

func TestNetworkContractRequiresKnownVersionAndParticipatesInHash(t *testing.T) {
	ds := &DesiredState{ServerID: 1, Revision: 1, Nodes: []NodeSpec{{NodeID: 1, ListenPort: 21001, Core: "singbox", Network: &NodeNetworkSpec{Policy: networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}}}}}
	ds.Hash = ContentHash(ds)
	if ValidateDesired(ds, 1, 0, "") == nil {
		t.Fatal("binding accepted without schema contract")
	}
	ds.NetworkBindingVersion = NetworkBindingVersion
	ds.Hash = ContentHash(ds)
	if err := ValidateDesired(ds, 1, 0, ""); err != nil {
		t.Fatal(err)
	}
	// Even an empty cleanup state retains the contract in its hash. An old
	// decoder that drops the field cannot reproduce that hash.
	ds.Nodes = []NodeSpec{}
	ds.Hash = ContentHash(ds)
	legacy := *ds
	legacy.NetworkBindingVersion = 0
	if ContentHash(&legacy) == ds.Hash {
		t.Fatal("legacy decoder can validate bound-server cleanup")
	}
	for _, version := range []int{-1, NetworkBindingVersion + 1} {
		ds.NetworkBindingVersion = version
		ds.Hash = ContentHash(ds)
		if ValidateDesired(ds, 1, 0, "") == nil {
			t.Fatal("unsupported network contract accepted")
		}
	}
}

func TestSOCKS5WireRequiresIndependentContract(t *testing.T) {
	resolver := networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}
	network := &NodeNetworkSpec{
		Policy: networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1},
		SOCKS5: &SOCKS5Egress{Config: networkconfig.SOCKS5{Server: "upstream.example.test", ServerPort: 1080, Authentication: "password", Family: "dual", DNS: resolver, Outer: networkconfig.Direct{Family: "ipv4", DNS: resolver}, ConnectTimeoutSeconds: 10}, Credentials: networkconfig.SOCKS5Credentials{Username: "fixture", Password: "fixture-only"}},
	}
	ds := &DesiredState{ServerID: 1, Revision: 1, NetworkBindingVersion: 1, Nodes: []NodeSpec{{NodeID: 1, ListenPort: 21001, Core: "singbox", Network: network}}}
	ds.Hash = ContentHash(ds)
	if ValidateDesired(ds, 1, 0, "") == nil {
		t.Fatal("direct binding support implied transit support")
	}
	ds.NetworkEgressVersion = NetworkEgressVersion
	ds.Hash = ContentHash(ds)
	if err := ValidateDesired(ds, 1, 0, ""); err != nil {
		t.Fatal(err)
	}
	before := ds.Hash
	network.SOCKS5.Credentials.Password += "-rotated"
	ds.Hash = ContentHash(ds)
	if ds.Hash == before {
		t.Fatal("credential rotation did not change the desired hash")
	}
	if network.OuterBinding() != &network.SOCKS5.Config.Outer {
		t.Fatal("SOCKS5 outer interface binding lost")
	}
	network.Direct = network.OuterBinding()
	if network.Validate() == nil {
		t.Fatal("ambiguous direct and transit accepted")
	}
	network.Direct = nil
	network.SOCKS5.Credentials = networkconfig.SOCKS5Credentials{}
	if network.Validate() == nil {
		t.Fatal("password authentication accepted without credentials")
	}
	ds.Nodes = nil
	ds.Hash = ContentHash(ds)
	old := *ds
	old.NetworkEgressVersion = 0
	if ContentHash(&old) == ds.Hash {
		t.Fatal("old agent could validate transport cleanup")
	}
	for _, version := range []int{-1, 2} {
		ds.NetworkEgressVersion = version
		ds.Hash = ContentHash(ds)
		if ValidateDesired(ds, 1, 0, "") == nil {
			t.Fatal("unknown egress contract accepted")
		}
	}
	ds.NetworkEgressVersion, ds.NetworkBindingVersion = 1, 0
	ds.Hash = ContentHash(ds)
	if ValidateDesired(ds, 1, 0, "") == nil {
		t.Fatal("egress contract accepted without binding contract")
	}
}
