package subscription

import (
	"strings"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func TestShareUserinfoAllQuotaModes(t *testing.T) {
	for _, tc := range []struct {
		mode           string
		up, down, used int64
	}{
		{domain.BillingDual, 900, 600, 1500},
		{domain.BillingUp, 900, 0, 900},
		{domain.BillingDown, 0, 600, 600},
		{domain.BillingMax, 900, 0, 900},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			sh := domain.Share{BillingMode: tc.mode, UsedUpload: 900, UsedDownload: 600, QuotaBytes: 2000}
			u := shareUserinfo(sh)
			parsed, ok := ParseUserinfo(u.Header())
			if !ok || parsed.Upload != tc.up || parsed.Download != tc.down || parsed.Upload+parsed.Download != tc.used || parsed.Remaining() != 2000-tc.used {
				t.Fatal("header inconsistent with billed usage", u.Header(), parsed)
			}
			lines := u.InfoLines(time.Now())
			if len(lines) != 1 || !strings.Contains(lines[0], FormatBytes(2000-tc.used)) {
				t.Fatal("info node inconsistent with header", lines)
			}
			if sh.UsedUpload != 900 || sh.UsedDownload != 600 {
				t.Fatal("projecting a header changed raw counters")
			}
		})
	}
}

func TestParseUserinfoRejectsInvalidTraffic(t *testing.T) {
	for _, h := range []string{
		"upload=-1; download=2",
		"upload=1; download=-2",
		"upload=1.5; download=2",
		"upload=1e6; download=2",
		"upload=9223372036854775808; download=0",
		"upload=9223372036854775807; download=1",
		"upload=NaN; download=2",
		"upload=Inf; download=2",
		"upload=1; total=-1",
		"upload=1; expire=-1",
	} {
		if u, ok := ParseUserinfo(h); ok {
			t.Fatal("invalid header accepted", h, u)
		}
	}
	u, ok := ParseUserinfo("unknown=ignore; upload=9223372036854775806; download=1; total=9223372036854775807")
	if !ok || u.Remaining() != 0 {
		t.Fatal("valid exact int64 boundary rejected", u, ok)
	}
}
