package store

import (
	"context"
	"math"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func TestShareSettingsPreserveRuntimeFields(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	sh := domain.Share{Name: "fixture", BillingMode: domain.BillingDual, UsedUpload: 10, UsedDownload: 20}
	if err := st.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	stale := sh
	newPeriod := sh.PeriodStart.Add(time.Hour)
	if err := st.ResetShareUsage(ctx, sh.ID, newPeriod); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AddShareUsage(ctx, sh.ID, 300, 400); err != nil {
		t.Fatal(err)
	}
	if err := st.SetShareStatus(ctx, sh.ID, domain.SharePaused); err != nil {
		t.Fatal(err)
	}
	stale.Name, stale.BillingMode = "edited", domain.BillingMax
	if err := st.UpdateShareSettings(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.Name != "edited" || got.BillingMode != domain.BillingMax || got.UsedUpload != 300 || got.UsedDownload != 400 || !got.PeriodStart.Equal(newPeriod) || got.Status != domain.SharePaused {
		t.Fatal("settings save rewrote a newer runtime state", got, err)
	}
}

func TestShareUsageRejectsOverflowWithoutChangingTotals(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	sh := domain.Share{Name: "fixture", UsedUpload: math.MaxInt64 - 2, UsedDownload: 1}
	if err := st.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	for _, delta := range [][2]int64{{2, 0}, {0, 2}, {-1, 0}, {math.MaxInt64, 1}} {
		if _, _, err := st.AddShareUsage(ctx, sh.ID, delta[0], delta[1]); err == nil {
			t.Fatal("invalid usage accepted", delta)
		}
		got, err := st.GetShare(ctx, sh.ID)
		if err != nil || got.UsedUpload != sh.UsedUpload || got.UsedDownload != sh.UsedDownload {
			t.Fatal("rejected delta changed current usage", got, err)
		}
	}
	if up, down, err := st.AddShareUsage(ctx, sh.ID, 1, 0); err != nil || domain.Total(up, down) != math.MaxInt64 {
		t.Fatal("exact representable boundary was rejected", up, down, err)
	}
}

func TestShareEvaluatedStatusRejectsEveryStaleAccountingInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*domain.Share)
	}{
		{"mode", func(sh *domain.Share) { sh.BillingMode = domain.BillingMax }},
		{"quota", func(sh *domain.Share) { sh.QuotaBytes++ }},
		{"reset day", func(sh *domain.Share) { sh.ResetDay = 2 }},
		{"expiry", func(sh *domain.Share) { expires := sh.PeriodStart.Add(time.Hour); sh.ExpiresAt = &expires }},
		{"inbound", func(sh *domain.Share) { sh.UsedUpload++ }},
		{"outbound", func(sh *domain.Share) { sh.UsedDownload++ }},
		{"period", func(sh *domain.Share) { sh.PeriodStart = sh.PeriodStart.Add(time.Hour) }},
		{"paused", func(sh *domain.Share) { sh.Status = domain.SharePaused }},
		{"revoked", func(sh *domain.Share) { sh.Status = domain.ShareRevoked }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openTest(t)
			ctx := context.Background()
			sh := domain.Share{Name: "fixture", BillingMode: domain.BillingDual, QuotaBytes: 1000, ResetDay: 1, UsedUpload: 900, UsedDownload: 600}
			if err := st.CreateShare(ctx, &sh); err != nil {
				t.Fatal(err)
			}
			changed := sh
			tc.edit(&changed)
			if err := st.UpdateShare(ctx, &changed); err != nil {
				t.Fatal(err)
			}
			if applied, err := st.SetShareEvaluatedStatus(ctx, sh, domain.ShareExhausted); err != nil || applied {
				t.Fatal("stale evaluation overwrote a newer accounting input", applied, err)
			}
			got, err := st.GetShare(ctx, sh.ID)
			if err != nil || got.Status != changed.Status {
				t.Fatal("rejected evaluation changed status", got.Status, err)
			}
			if changed.Status != domain.SharePaused && changed.Status != domain.ShareRevoked {
				if applied, err := st.SetShareEvaluatedStatus(ctx, changed, domain.ShareExhausted); err != nil || !applied {
					t.Fatal("current snapshot failed to commit", applied, err)
				}
			} else if applied, err := st.SetShareEvaluatedStatus(ctx, changed, domain.ShareActive); err != nil || applied {
				t.Fatal("automatic transition overrode a manual state", applied, err)
			}
		})
	}
}

func TestShareCalendarResetPreservesManualResetAndChangedSettings(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	sh := domain.Share{Name: "fixture", ResetDay: 1, PeriodStart: start.Add(time.Hour), UsedUpload: 300, UsedDownload: 400}
	if err := st.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.ResetShareUsageIfBefore(ctx, sh.ID, 1, start); err != nil || changed {
		t.Fatal("calendar reset overwrote a later manual reset", changed, err)
	}
	if changed, err := st.ResetShareUsageIfBefore(ctx, sh.ID, 2, start.AddDate(0, 1, 0)); err != nil || changed {
		t.Fatal("old reset setting overwrote a changed setting", changed, err)
	}
	got, err := st.GetShare(ctx, sh.ID)
	if err != nil || got.UsedUpload != 300 || got.UsedDownload != 400 || !got.PeriodStart.Equal(sh.PeriodStart) {
		t.Fatal("stale calendar evaluation changed current usage", got, err)
	}
}
