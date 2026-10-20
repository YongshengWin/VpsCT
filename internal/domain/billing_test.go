package domain

import (
	"math"
	"testing"
)

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

func TestServerQuotaBilling(t *testing.T) {
	for _, tc := range []struct {
		input, normalized string
		billed            int64
	}{
		{"", BillingDual, 800},
		{BillingSum, BillingDual, 800},
		{BillingDual, BillingDual, 800},
		{BillingUp, BillingUp, 300},
		{BillingDown, BillingDown, 500},
		{BillingMax, BillingMax, 500},
	} {
		mode, ok := NormalizeServerQuotaBilling(tc.input)
		if !ok || mode != tc.normalized {
			t.Fatalf("normalize %q: %q %v", tc.input, mode, ok)
		}
		if got := ServerQuotaBilled(mode, 300, 500); got != tc.billed {
			t.Fatalf("billed %q: %d, want %d", mode, got, tc.billed)
		}
	}
	if _, ok := NormalizeServerQuotaBilling("both"); ok {
		t.Fatal("unknown server quota mode accepted")
	}
	if got := ServerQuotaBilled(BillingMax, 700, 500); got != 700 {
		t.Fatal("max did not choose inbound", got)
	}
}

func TestUnifiedQuotaBillingAndHeaderProjection(t *testing.T) {
	for _, tc := range []struct {
		mode             string
		billed, up, down int64
	}{
		{BillingDual, 1500, 900, 600},
		{BillingUp, 900, 900, 0},
		{BillingDown, 600, 0, 600},
		{BillingMax, 900, 900, 0},
		{BillingSum, 1500, 900, 600},
		{"", 1500, 900, 600},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			wantMode, ok := NormalizeQuotaBilling(tc.mode)
			if !ok {
				t.Fatal("valid quota mode rejected")
			}
			for _, normalize := range []func(string) (string, bool){NormalizeBilling, NormalizeServerQuotaBilling} {
				if mode, ok := normalize(tc.mode); !ok || mode != wantMode {
					t.Fatal("server and share normalization diverged", mode, ok)
				}
			}
			for _, billed := range []func(string, int64, int64) int64{QuotaBilled, Billed, ServerQuotaBilled} {
				if got := billed(tc.mode, 900, 600); got != tc.billed {
					t.Fatalf("billed=%d want=%d", got, tc.billed)
				}
			}
			up, down := QuotaUserinfoDirections(tc.mode, 900, 600)
			if up != tc.up || down != tc.down || up+down != tc.billed {
				t.Fatal("header and quota usage diverged", up, down)
			}
		})
	}
	if up, down := QuotaUserinfoDirections(BillingMax, 100, 300); up != 0 || down != 300 {
		t.Fatal("max outbound projection", up, down)
	}
	if up, down := QuotaUserinfoDirections(BillingMax, 100, 100); up != 100 || down != 0 {
		t.Fatal("max tie projection", up, down)
	}
	// Two resources with opposite larger directions: max(800+100,100+500)
	// is 900, while summing their maxima would incorrectly bill 1300.
	if got := QuotaBilled(BillingMax, 800+100, 100+500); got != 900 {
		t.Fatal("max did not use aggregated direction totals", got)
	}
}

func TestTrafficTotalsRejectNegativeAndOverflow(t *testing.T) {
	for _, totals := range [][2]int64{{-1, 0}, {0, -1}, {math.MaxInt64, 1}, {1, math.MaxInt64}} {
		if ValidTrafficTotals(totals[0], totals[1]) {
			t.Fatal("invalid traffic total accepted", totals)
		}
	}
	for _, totals := range [][2]int64{{0, 0}, {math.MaxInt64, 0}, {1, math.MaxInt64 - 1}} {
		if !ValidTrafficTotals(totals[0], totals[1]) {
			t.Fatal("exact representable traffic total rejected", totals)
		}
	}
}
