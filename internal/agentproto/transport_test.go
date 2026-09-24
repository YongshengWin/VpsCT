package agentproto

import (
	"encoding/json"
	"strings"
	"testing"

	"ctlvps/internal/networkconfig"
)

func TestTransportAuthorityOnlyTravelsInValidatedDiagnostics(t *testing.T) {
	g := networkconfig.TransportGrant{NodeID: 1, EgressProfileID: 2, Purpose: "socks5", Network: "tcp", Address: "10.23.0.2", Port: 1080}
	d := Diagnostics{NetworkTransportVersion: NetworkTransportVersion, TransportGrants: []networkconfig.TransportGrant{g}}
	if err := ValidateNetworkDiagnostics(d); err != nil {
		t.Fatal(err)
	}
	d.NetworkTransportVersion = 0
	if ValidateNetworkDiagnostics(d) == nil {
		t.Fatal("grant accepted from agent without transport capability")
	}
	d.NetworkTransportVersion = NetworkTransportVersion
	d.TransportGrants[0].Address = "0.0.0.0/0"
	if ValidateNetworkDiagnostics(d) == nil {
		t.Fatal("overbroad grant observation accepted")
	}
	n := NodeSpec{NodeID: 1, TransportGrants: []networkconfig.TransportGrant{g}}
	raw, err := json.Marshal(n)
	if err != nil || strings.Contains(string(raw), "transport_grants") || strings.Contains(string(raw), g.Address) {
		t.Fatal("local authority leaked into desired JSON", err)
	}
	if err = json.Unmarshal([]byte(`{"node_id":1,"transport_grants":[{"node_id":1}]}`), &n); err != nil {
		t.Fatal(err)
	}
	if len(n.TransportGrants) != 1 || n.TransportGrants[0] != g {
		t.Fatal("remote JSON changed local authority")
	}
}
