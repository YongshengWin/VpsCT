package secureupdate

import (
	"encoding/json"
	"testing"

	"ctlvps/internal/networkconfig"
)

func TestLocalPolicyValidatesTransportGrantsBeforeDefaultTrustMode(t *testing.T) {
	g := networkconfig.TransportGrant{NodeID: 7, EgressProfileID: 3, Purpose: "socks5", Network: "tcp", Address: "10.20.0.0/24", Port: 1080}
	p := Policy{Schema: 1, Actions: []string{"agent.configure"}, TransportGrants: []networkconfig.TransportGrant{g}}
	b, _ := json.Marshal(p)
	got, err := decodePolicy(b)
	if err != nil || !got.ChecksumOnly || len(got.PrivateNodes) != 0 || len(got.TransportGrants) != 1 {
		t.Fatal("valid narrow policy changed business permission", err)
	}
	for _, edit := range []func(*Policy){
		func(p *Policy) { p.TransportGrants[0].Address = "0.0.0.0/0" },
		func(p *Policy) { p.TransportGrants[0].Purpose = "business" },
		func(p *Policy) { p.TransportGrants[0].NodeID = 0 },
	} {
		var invalid Policy
		json.Unmarshal(b, &invalid)
		edit(&invalid)
		raw, _ := json.Marshal(invalid)
		if _, err := decodePolicy(raw); err == nil {
			t.Fatal("checksum-only branch bypassed endpoint validation")
		}
	}
	if _, err := decodePolicy(append(b, []byte(` {"schema":1}`)...)); err == nil {
		t.Fatal("extra policy document accepted")
	}
}
