package nft

import (
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
)

func TestForwardCountersRetainIdentityAndRejectIncompleteReadings(t *testing.T) {
	meters := []agentproto.ForwardMeterRule{{ForwardID: 1, ListenPort: 24001}, {ForwardID: 2, Retired: true, Blocked: true}}
	rules, err := ForwardRules(meters)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rules, "delete counter") || strings.Contains(rules, "reset counter") || strings.Contains(rules, "0x43000001") || strings.Contains(rules, "counter name f2_") || !strings.Contains(rules, "0x45000001 counter name f1_rx") {
		t.Fatal("forward counter namespace or retirement changed", rules)
	}
	valid := `{"nftables":[{"counter":{"name":"f1_rx","bytes":100,"packets":2}},{"counter":{"name":"f1_tx","bytes":200,"packets":3}}]}`
	readings, err := parseForwardCounters([]byte(valid))
	if err != nil || len(readings) != 1 || readings[0].ForwardID != 1 || readings[0].Rx != 100 || readings[0].Tx != 200 {
		t.Fatal(readings, err)
	}
	for _, bad := range []string{strings.Replace(valid, "f1_tx", "f1_rx", 1), strings.Replace(valid, "f1_tx", "f2_tx", 1), strings.Replace(valid, "f1_rx", "n1_rx", 1), strings.Replace(valid, "100", "-1", 1)} {
		if _, err := parseForwardCounters([]byte(bad)); err == nil {
			t.Fatal("corrupt counter snapshot accepted")
		}
	}
}
