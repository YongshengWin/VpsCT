package domain

import "testing"

func TestBilled(t *testing.T) {
	const up, down int64 = 300, 500
	if got := Billed(BillingSum, up, down); got != 800 {
		t.Fatalf("billed: %d", got)
	}
	if got := Billed("", up, down); got != 800 {
		t.Fatalf("empty: %d", got)
	}
	if Inbound(up, down) != 300 || Outbound(up, down) != 500 || Total(up, down) != 800 {
		t.Fatal("in/out/total")
	}
}

func TestNormalizeResetDay(t *testing.T) {
	if NormalizeResetDay(0) != 0 || NormalizeResetDay(15) != 15 || NormalizeResetDay(28) != 28 {
		t.Fatal("1-28")
	}
	if NormalizeResetDay(29) != 31 || NormalizeResetDay(30) != 31 || NormalizeResetDay(31) != 31 {
		t.Fatal("29-31 should be last day")
	}
}

func TestNormalizeBilling(t *testing.T) {
	if got, ok := NormalizeBilling(""); !ok || got != BillingDual {
		t.Fatalf("empty: %s %v", got, ok)
	}
	if got, ok := NormalizeBilling(BillingSum); !ok || got != BillingDual {
		t.Fatalf("legacy sum: %s %v", got, ok)
	}
	if _, ok := NormalizeBilling("both"); ok {
		t.Fatal("unknown mode should fail")
	}
}
