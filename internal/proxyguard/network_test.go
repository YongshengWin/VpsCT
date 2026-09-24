package proxyguard

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNetworkDigestAllowsOnlyExpiringReadiness(t *testing.T) {
	for _, resource := range []string{"n1_ready", "f1_ready"} {
		t.Run(resource, func(t *testing.T) { testNetworkLeaseDigest(t, resource) })
	}
}

func testNetworkLeaseDigest(t *testing.T, resource string) {
	t.Helper()
	base := `{"nftables":[{"table":{"family":"inet","name":"ctlvps_network"}},{"set":{"family":"inet","table":"ctlvps_network","name":"n1_ready","type":"nf_proto","flags":["timeout"],"size":2,"timeout":6,"gc-interval":1}}]}`
	base = strings.Replace(base, "n1_ready", resource, 1)
	want, err := networkDigest([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	var shuffled struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(base), &shuffled); err != nil {
		t.Fatal(err)
	}
	shuffled.Nftables[0], shuffled.Nftables[1] = shuffled.Nftables[1], shuffled.Nftables[0]
	raw, _ := json.Marshal(shuffled)
	if got, err := networkDigest(raw); err != nil || got != want {
		t.Fatal("nft declaration ordering changed enforcement digest", err)
	}
	with := func(elements string) string {
		return strings.Replace(base, `"gc-interval":1`, `"gc-interval":1,"elem":`+elements, 1)
	}
	for _, s := range []string{`[]`, `[{"elem":{"val":"ipv4","expires":5}},{"elem":{"val":"ipv6","expires":0}}]`} {
		got, err := networkDigest([]byte(with(s)))
		if err != nil || got != want {
			t.Fatalf("lease changed static policy: %v", err)
		}
	}
	for _, s := range []string{`["ipv4"]`, `[{"elem":{"val":"ipv4"}}]`, `[{"elem":{"val":"ipv4","expires":7}}]`, `[{"elem":{"val":"ipv4","expires":5,"timeout":0}}]`, `[{"elem":{"val":"ipv4","expires":5,"timeout":60}}]`, `[{"elem":{"val":"arp","expires":5}}]`} {
		if _, err := networkDigest([]byte(with(s))); err == nil {
			t.Errorf("unsafe lease accepted: %s", s)
		}
	}
	if _, err := networkDigest([]byte(strings.Replace(base, `"timeout":6`, `"timeout":60`, 1))); err == nil {
		t.Fatal("unbounded set accepted")
	}
}
