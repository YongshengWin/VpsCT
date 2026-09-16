package nft

import "testing"

func TestParseCounterName(t *testing.T) {
	cases := []struct {
		name    string
		port    int
		inbound bool
		ok      bool
	}{
		{"in_443", 443, true, true},
		{"out_443", 443, false, true},
		{"orig_in_443", 443, true, true},
		{"orig_out_8443", 8443, false, true},
		{"other_1", 0, false, false},
		{"in_", 0, false, false},
	}
	for _, c := range cases {
		port, inbound, ok := parseCounterName(c.name)
		if ok != c.ok || (ok && (port != c.port || inbound != c.inbound)) {
			t.Fatalf("%s: port=%d in=%v ok=%v", c.name, port, inbound, ok)
		}
	}
}
