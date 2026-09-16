package proxyguard

import "testing"

func TestPolicyDigest(t *testing.T) {
	a := []byte(`{"nftables":[{"metainfo":{"version":"old"}},{"table":{"family":"inet","name":"ctlvps_egress","handle":1}},{"rule":{"handle":3,"expr":[{"drop":null}]}}]}`)
	b := []byte(`{"nftables":[{"metainfo":{"version":"new"}},{"table":{"family":"inet","name":"ctlvps_egress","handle":9}},{"rule":{"handle":11,"expr":[{"drop":null}]}}]}`)
	c := []byte(`{"nftables":[{"table":{"family":"inet","name":"ctlvps_egress"}},{"rule":{"expr":[{"accept":null}]}}]}`)
	x, e := digest(a)
	if e != nil {
		t.Fatal(e)
	}
	y, e := digest(b)
	if e != nil || x != y {
		t.Fatal("reload metadata changed digest", e)
	}
	z, e := digest(c)
	if e != nil || z == x {
		t.Fatal("changed enforcement was not detected", e)
	}
	if _, e = digest([]byte(`{"nftables":[]}`)); e == nil {
		t.Fatal("empty table accepted")
	}
}
func TestGuardCannotTargetController(t *testing.T) {
	for _, s := range []string{"ctlvpsd.service", "ctlvps-agent.service", "ctlvps-maintenance.service", "ctlvps-snell@0.service", "ctlvps-snell@01.service", "ctlvps-snell@65536.service", "ctlvps-singbox.service extra"} {
		if validUnit(s) {
			t.Fatalf("unsafe target %q", s)
		}
	}
	for _, s := range []string{"ctlvps-singbox.service", "ctlvps-singbox-private.service", "ctlvps-snell@24443.service"} {
		if !validUnit(s) {
			t.Fatal(s)
		}
	}
}
