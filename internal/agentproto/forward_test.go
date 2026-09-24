package agentproto

import (
	"encoding/json"
	"strings"
	"testing"

	"ctlvps/internal/networkconfig"
)

func TestForwardIdentityAndRuntimeAdmissionRemainSeparate(t *testing.T) {
	node, _ := NodeMark(1)
	forward, err := (ResourceIdentity{Kind: "forward", ID: 1}).Mark()
	if err != nil || node != 0x43000001 || forward != 0x45000001 {
		t.Fatal(node, forward, err)
	}
	if _, err = (ResourceIdentity{Kind: "unknown", ID: 1}).Mark(); err == nil {
		t.Fatal("unknown resource admitted")
	}
	legacy := &DesiredState{ServerID: 1, Revision: 1}
	legacy.Hash = ContentHash(legacy)
	wire, _ := json.Marshal(legacy)
	if strings.Contains(string(wire), "forward") {
		t.Fatal("legacy payload changed")
	}
	f := ForwardSpec{ForwardID: 1, Revision: 1, Config: networkconfig.Forward{ListenMode: "all", ListenPort: 24443, Network: "tcp", TargetHost: "192.0.2.2", TargetPort: 443, SourceMode: "cidr", MaxTCPConnections: 32}, RuntimeNetwork: &networkconfig.Resolved{BootID: "local-only"}}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	wire, _ = json.Marshal(f)
	if strings.Contains(string(wire), "local-only") {
		t.Fatal("local runtime crossed wire")
	}
	legacy.Forwards, legacy.NetworkForwardVersion = []ForwardSpec{f}, 1
	legacy.Hash = ContentHash(legacy)
	if err := ValidateDesired(legacy, 1, 0, ""); err == nil {
		t.Fatal("forwarding admitted without binding contract")
	}
	legacy.NetworkBindingVersion = NetworkBindingVersion
	legacy.Hash = ContentHash(legacy)
	if err := ValidateDesired(legacy, 1, 0, ""); err != nil {
		t.Fatal("compatible TCP forward rejected", err)
	}
	legacy.Forwards[0].Config.TargetHost = "unresolved.example.test"
	legacy.Hash = ContentHash(legacy)
	if err := ValidateDesired(legacy, 1, 0, ""); err == nil {
		t.Fatal("unresolved forward admitted")
	}
	legacy.Forwards[0].Config.EgressProfileID, legacy.Forwards[0].Config.EgressRevision = 1, 1
	legacy.Forwards[0].Direct = &networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}
	legacy.Hash = ContentHash(legacy)
	if err := ValidateDesired(legacy, 1, 0, ""); err != nil {
		t.Fatal("domain target with explicit public resolver rejected", err)
	}
	legacy.Forwards[0].Direct.DNS.Address = "10.0.0.53"
	legacy.Hash = ContentHash(legacy)
	if err := ValidateDesired(legacy, 1, 0, ""); err == nil {
		t.Fatal("private forward resolver admitted without authorization")
	}
}
