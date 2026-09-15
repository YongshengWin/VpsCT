package core

import "testing"

func TestParseIPAccounting(t *testing.T) {
	in, out, ok := parseIPAccounting("IPIngressBytes=100\nIPEgressBytes=200\n")
	if !ok || in != 100 || out != 200 {
		t.Fatalf("got %d %d %v", in, out, ok)
	}
	if _, _, ok := parseIPAccounting("IPIngressBytes=[not set]\nIPEgressBytes=[not set]\n"); ok {
		t.Fatal("unset must fail")
	}
}
