package nft

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
)

func TestForeignDestinationPortProof(t *testing.T) {
	portMatch := func(protocol, field, op, value string) string {
		return fmt.Sprintf(`{"match":{"op":%q,"left":{"payload":{"protocol":%q,"field":%q}},"right":%s}}`, op, protocol, field, value)
	}
	dport := portMatch("tcp", "dport", "==", "22")
	address := `{"match":{"op":"==","left":{"payload":{"protocol":"ip","field":"saddr"}},"right":"@addr-set-sshd"}}`
	ports := map[string]map[int]bool{"tcp": {443: true}, "udp": {22: true}}
	for _, tc := range []struct {
		name, policy, expressions string
		want                      bool
	}{
		{"fail2ban", "accept", dport + "," + address + `,{"reject":{"type":"icmpx","expr":"port-unreachable"}}`, true},
		{"iptables address first", "accept", address + "," + dport + `,{"counter":{}},{"accept":null}`, true},
		{"direct drop", "accept", dport + `,{"drop":null}`, true},
		{"jump", "accept", dport + `,{"jump":{"target":"custom"}}`, true},
		{"set ranges", "accept", portMatch("tcp", "dport", "==", `{"set":[22,{"range":[100,200]}]}`) + `,{"drop":null}`, true},
		{"protocol overlap", "accept", portMatch("udp", "dport", "==", "22") + `,{"drop":null}`, false},
		{"boundary overlap", "accept", portMatch("tcp", "dport", "==", `{"range":[22,443]}`) + `,{"drop":null}`, false},
		{"source port", "accept", portMatch("tcp", "sport", "==", "22") + `,{"drop":null}`, false},
		{"negated port", "accept", portMatch("tcp", "dport", "!=", "22") + `,{"drop":null}`, false},
		{"named port set", "accept", portMatch("tcp", "dport", "==", `"@ports"`) + `,{"drop":null}`, false},
		{"zero", "accept", portMatch("tcp", "dport", "==", "0") + `,{"drop":null}`, false},
		{"out of range", "accept", portMatch("tcp", "dport", "==", "65536") + `,{"drop":null}`, false},
		{"inverted range", "accept", portMatch("tcp", "dport", "==", `{"range":[200,100]}`) + `,{"drop":null}`, false},
		{"nested set", "accept", portMatch("tcp", "dport", "==", `{"set":[{"set":[22]}]}`) + `,{"drop":null}`, false},
		{"empty set", "accept", portMatch("tcp", "dport", "==", `{"set":[]}`) + `,{"drop":null}`, false},
		{"default drop", "drop", dport + `,{"accept":null}`, false},
		{"missing policy", "", dport + `,{"accept":null}`, false},
		{"whole chain drop", "accept", `{"drop":null}`, false},
		{"early jump", "accept", `{"jump":{"target":"custom"}},` + dport + `,{"drop":null}`, false},
		{"early mutation", "accept", `{"mangle":{}},` + dport + `,{"drop":null}`, false},
		{"unknown after match", "accept", dport + `,{"xt":{}},{"drop":null}`, false},
		{"missing action", "accept", dport, false},
		{"empty rule", "accept", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rule ingressEntry
			if err := json.Unmarshal([]byte(`{"expr":[`+tc.expressions+`]}`), &rule); err != nil {
				t.Fatal(err)
			}
			chain := &ingressEntry{Family: "inet", Table: "f2b-table", Name: "f2b-chain", Policy: tc.policy}
			if got := compatInputDisjoint(chain, []*ingressEntry{&rule}, ports, ""); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			if tc.want {
				if compatInputDisjoint(chain, []*ingressEntry{&rule, {}}, ports, "") {
					t.Fatal("unconstrained second rule accepted")
				}
			}
		})
	}
}

func TestForeignPortProofIntegratesWithoutRewritingOtherTables(t *testing.T) {
	for _, family := range []string{"ip", "ip6", "inet"} {
		extra := fmt.Sprintf(`,{"chain":{"family":%q,"table":"foreign","name":"input","hook":"input","policy":"accept"}},{"rule":{"family":%q,"table":"foreign","chain":"input","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":22}},{"drop":null}]}}`, family, family)
		got, err := IngressRules(ingressSnapshot(extra), []agentproto.NodeSpec{{Protocol: "vless", ListenPort: 443}})
		if err != nil || !strings.Contains(got, "tcp dport { 443 }") || strings.Contains(got, "foreign") {
			t.Fatalf("%s: %q %v", family, got, err)
		}
		if _, err := IngressRules(ingressSnapshot(extra), []agentproto.NodeSpec{{Protocol: "vless", ListenPort: 22}}); err == nil {
			t.Fatal("overlap accepted")
		}
	}
}
