package traffic_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

func TestServerUsageCalibrationCountsOnlyTrafficAfterSnapshotAndExpires(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	ing := traffic.New(st)
	ing.Now = st.Now
	srv := domain.Server{Name: "fixture", QuotaBytes: 200, QuotaResetDay: 8, Enabled: true}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now.AddDate(0, 0, -12), 50, 62); err != nil {
		t.Fatal(err)
	}
	usage, err := ing.ServerUsage(ctx, srv)
	if err != nil || usage.Total != 112 || usage.Billed != 112 {
		t.Fatalf("initial usage: %+v %v", usage, err)
	}
	target := int64(150)
	if err = st.UpdateServerWithCalibration(ctx, &srv, &target, traffic.PeriodStart(now, srv.QuotaResetDay)); err != nil {
		t.Fatal(err)
	}
	usage, err = ing.ServerUsage(ctx, srv)
	if err != nil || usage.Total != 112 || usage.Billed != 150 || usage.QuotaCalibratedUsageBytes == nil || *usage.QuotaCalibratedUsageBytes != 150 || usage.QuotaCalibrationObservedBytes == nil || *usage.QuotaCalibrationObservedBytes != 112 {
		t.Fatalf("snapshot counted previous observations twice: %+v %v", usage, err)
	}
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now, 4, 6); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	usage, err = ing.ServerUsage(ctx, srv)
	if err != nil || usage.Total != 122 || usage.Billed != 160 {
		t.Fatalf("new traffic or ordinary edit changed calibration: %+v %v", usage, err)
	}
	now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now, 5, 5); err != nil {
		t.Fatal(err)
	}
	usage, err = ing.ServerUsage(ctx, srv)
	if err != nil || usage.Total != 10 || usage.Billed != 10 || usage.QuotaCalibratedUsageBytes != nil || usage.NextReset == nil || !usage.NextReset.Equal(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("prior-period calibration leaked: %+v %v", usage, err)
	}
}

func TestServerUsageCalibrationZeroAndResetDayChange(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	ing := traffic.New(st)
	ing.Now = st.Now
	srv := domain.Server{Name: "fixture", QuotaResetDay: 8, Enabled: true}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now, 10, 10); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	if err = st.UpdateServerWithCalibration(ctx, &srv, &zero, traffic.PeriodStart(now, srv.QuotaResetDay)); err != nil {
		t.Fatal(err)
	}
	usage, err := ing.ServerUsage(ctx, srv)
	if err != nil || usage.Billed != 0 || usage.QuotaCalibratedUsageBytes == nil || *usage.QuotaCalibratedUsageBytes != 0 {
		t.Fatalf("explicit zero must be a calibration: %+v %v", usage, err)
	}
	srv.QuotaResetDay = 9
	if err = st.UpdateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	srv.QuotaResetDay = 8
	if err = st.UpdateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	usage, err = ing.ServerUsage(ctx, srv)
	if err != nil || usage.Billed != 20 || usage.QuotaCalibratedUsageBytes != nil {
		t.Fatalf("changed reset day resurrected an old calibration: %+v %v", usage, err)
	}
}

func TestServerPeriodDailyCoversFullMonthWithoutInventingUsage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	ing := traffic.New(st)
	ing.Now = st.Now
	srv := domain.Server{Name: "fixture", QuotaResetDay: 8, Enabled: true}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	start := traffic.PeriodStart(now, srv.QuotaResetDay)
	if !start.Equal(time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(start)
	}
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, start, 3, 4); err != nil {
		t.Fatal(err)
	}
	series, err := ing.DailySince(ctx, store.SubjectServer, srv.ID, start)
	if err != nil || series.From != "2026-08-08" || series.To != "2026-09-07" || len(series.Points) != 31 || series.Total != 7 || series.Points[1].Up != 0 {
		t.Fatalf("period series: %+v %v", series, err)
	}
	if got := traffic.PeriodStart(time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC), 31); !got.Equal(time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("last-day reset boundary", got)
	}
}

func TestServerUsageWithoutResetDayMatchesThirtyDayChart(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	ing := traffic.New(st)
	ing.Now = st.Now
	srv := domain.Server{Name: "rolling-fixture", Enabled: true}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	first := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -29)
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, first.AddDate(0, 0, -1), 20, 0); err != nil {
		t.Fatal(err)
	}
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, first, 3, 4); err != nil {
		t.Fatal(err)
	}
	usage, err := ing.ServerUsage(ctx, srv)
	if err != nil || usage.Total != 7 || !usage.PeriodStart.Equal(first) || usage.NextReset != nil {
		t.Fatalf("rolling quota included an extra day: %+v %v", usage, err)
	}
	series, err := ing.Daily(ctx, store.SubjectServer, srv.ID, 30)
	if err != nil || series.Total != usage.Total || len(series.Points) != 30 {
		t.Fatalf("rolling quota and chart disagree: %+v %v", series, err)
	}
}

func TestServerQuotaModesAndCalibration(t *testing.T) {
	for _, tc := range []struct {
		mode               string
		observedAtSnapshot int64
		billedAfter        int64
	}{
		{domain.BillingDual, 150, 290},
		{domain.BillingUp, 100, 210},
		{domain.BillingDown, 50, 280},
		{domain.BillingMax, 100, 280},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "traffic.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			st.Now = func() time.Time { return now }
			ing := traffic.New(st)
			ing.Now = st.Now
			srv := domain.Server{Name: "fixture", QuotaResetDay: 8, QuotaBilling: tc.mode, QuotaBytes: 270, Enabled: true}
			if err = st.CreateServer(ctx, &srv); err != nil {
				t.Fatal(err)
			}
			if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now.AddDate(0, 0, -1), 100, 50); err != nil {
				t.Fatal(err)
			}
			target := int64(200)
			if err = st.UpdateServerWithCalibration(ctx, &srv, &target, traffic.PeriodStart(now, srv.QuotaResetDay)); err != nil {
				t.Fatal(err)
			}
			before, err := ing.ServerUsage(ctx, srv)
			if err != nil || before.Billed != target || before.QuotaCalibrationObservedBytes == nil || *before.QuotaCalibrationObservedBytes != tc.observedAtSnapshot {
				t.Fatalf("calibration baseline: %+v %v", before, err)
			}
			if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now, 10, 80); err != nil {
				t.Fatal(err)
			}
			after, err := ing.ServerUsage(ctx, srv)
			if err != nil || after.Inbound != 110 || after.Outbound != 130 || after.Total != 240 || after.Billed != tc.billedAfter || after.OverQuota != (tc.billedAfter >= 270) {
				t.Fatalf("billing mode %s: %+v %v", tc.mode, after, err)
			}
			// The maximum switches from inbound at calibration to outbound
			// later. A scalar max difference would count only 30 instead of
			// the conservative post-calibration increment of 80.
			if tc.mode == domain.BillingMax {
				srv.QuotaBilling = domain.BillingDown
				if err = st.UpdateServer(ctx, &srv); err != nil {
					t.Fatal(err)
				}
				changed, err := ing.ServerUsage(ctx, srv)
				if err != nil || changed.Billed != 130 || changed.QuotaCalibratedUsageBytes != nil {
					t.Fatalf("mode change retained incompatible calibration: %+v %v", changed, err)
				}
			}
		})
	}
}

func TestServerQuotaMaxUsesPeriodTotals(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	ing := traffic.New(st)
	ing.Now = st.Now
	srv := domain.Server{Name: "period-max", QuotaResetDay: 8, QuotaBilling: domain.BillingMax}
	if err = st.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now.AddDate(0, 0, -1), 8, 1); err != nil {
		t.Fatal(err)
	}
	if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now, 1, 8); err != nil {
		t.Fatal(err)
	}
	usage, err := ing.ServerUsage(ctx, srv)
	if err != nil || usage.Inbound != 9 || usage.Outbound != 9 || usage.Total != 18 || usage.Billed != 9 {
		t.Fatalf("max must be applied after summing the period: %+v %v", usage, err)
	}
}
