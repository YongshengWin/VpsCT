package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func serverQuotaFixture(t *testing.T, calibrated bool) (*Store, ServerQuotaSnapshot) {
	t.Helper()
	s := openTest(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	v := domain.Server{Name: "quota-fixture", Region: "fixture", Tags: []string{"keep"}, Notes: "keep", QuotaBytes: 100, QuotaResetDay: 8, QuotaBilling: domain.BillingMax, Enabled: true}
	ctx := context.Background()
	if err := s.CreateServer(ctx, &v); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTraffic(ctx, SubjectServer, v.ID, now, 150, 50); err != nil {
		t.Fatal(err)
	}
	period := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	snapshot := ServerQuotaSnapshot{Server: v, ObservedAt: now, PeriodStart: period}
	if calibrated {
		amount := int64(160)
		if err := s.UpdateServerWithCalibration(ctx, &v, &amount, period, [2]int64{160, 80}); err != nil {
			t.Fatal(err)
		}
		c, found, err := s.GetServerUsageCalibration(ctx, v.ID, period)
		if err != nil || !found {
			t.Fatalf("calibration: %v, found=%v", err, found)
		}
		snapshot.Server, snapshot.Calibration = v, &c
	}
	return s, snapshot
}

func TestDisableServerQuotaPreservesSettingsAndCalibration(t *testing.T) {
	for _, calibrated := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw", true: "calibrated"}[calibrated], func(t *testing.T) {
			s, snapshot := serverQuotaFixture(t, calibrated)
			ctx := context.Background()
			// Same-period traffic can increase after evaluation without making an
			// already-over-quota decision invalid.
			if err := s.AddTraffic(ctx, SubjectServer, snapshot.Server.ID, s.Now(), 5, 8); err != nil {
				t.Fatal(err)
			}
			now := s.Now().Add(time.Minute)
			s.Now = func() time.Time { return now }
			changed, err := s.DisableServerIfQuotaUnchanged(ctx, snapshot, s.Now)
			if err != nil || !changed {
				t.Fatalf("disable: changed=%v err=%v", changed, err)
			}
			got, err := s.GetServer(ctx, snapshot.Server.ID)
			want := snapshot.Server
			want.Enabled, want.UpdatedAt = false, now
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("changed editable settings: got=%+v want=%+v err=%v", got, want, err)
			}
			c, found, err := s.GetServerUsageCalibration(ctx, got.ID, snapshot.PeriodStart)
			if err != nil || found != calibrated || (found && !reflect.DeepEqual(c, *snapshot.Calibration)) {
				t.Fatalf("changed calibration: %+v found=%v err=%v", c, found, err)
			}
			if changed, err = s.DisableServerIfQuotaUnchanged(ctx, snapshot, s.Now); err != nil || changed {
				t.Fatalf("repeated stop was not idempotent: changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestDisableServerQuotaRejectsEditedPolicy(t *testing.T) {
	cases := []struct {
		name string
		edit func(*domain.Server)
	}{
		{"quota", func(v *domain.Server) { v.QuotaBytes = 1000 }},
		{"mode", func(v *domain.Server) { v.QuotaBilling = domain.BillingDown }},
		{"reset", func(v *domain.Server) { v.QuotaResetDay = 9 }},
		{"manual-disable", func(v *domain.Server) { v.Enabled = false }},
		{"updated-at", func(v *domain.Server) { v.Name = "admin renamed" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, snapshot := serverQuotaFixture(t, false)
			ctx := context.Background()
			current := snapshot.Server
			if tc.name == "updated-at" {
				now := s.Now().Add(time.Minute)
				s.Now = func() time.Time { return now }
			}
			tc.edit(&current)
			if err := s.UpdateServer(ctx, &current); err != nil {
				t.Fatal(err)
			}
			changed, err := s.DisableServerIfQuotaUnchanged(ctx, snapshot, s.Now)
			got, getErr := s.GetServer(ctx, current.ID)
			if err != nil || changed || getErr != nil || !reflect.DeepEqual(got, current) {
				t.Fatalf("stale stop changed admin settings: changed=%v got=%+v want=%+v err=%v/%v", changed, got, current, err, getErr)
			}
		})
	}
}

func TestDisableServerQuotaRejectsReplacedCalibration(t *testing.T) {
	for _, change := range []string{"new-reading", "observed-direction", "provider-direction", "removed", "new-calibration", "calibrated-at", "period"} {
		t.Run(change, func(t *testing.T) {
			s, snapshot := serverQuotaFixture(t, change != "new-calibration")
			ctx := context.Background()
			current := snapshot.Server
			amount, pair := int64(160), [2]int64{160, 80}
			switch change {
			case "observed-direction":
				// Timestamp, scalar amount, observed max, and provider readings all
				// remain identical; the full directional baseline must still match.
				if err := s.AddTraffic(ctx, SubjectServer, current.ID, s.Now(), 0, 1); err != nil {
					t.Fatal(err)
				}
			case "new-reading":
				amount, pair[0] = 170, 170
			case "provider-direction":
				pair[1] = 81
			}
			var err error
			switch change {
			case "removed":
				_, err = s.DB().ExecContext(ctx, `DELETE FROM server_usage_calibrations WHERE server_id=?`, current.ID)
			case "calibrated-at":
				_, err = s.DB().ExecContext(ctx, `UPDATE server_usage_calibrations SET calibrated_at=? WHERE server_id=?`, fmtTime(s.Now().Add(time.Minute)), current.ID)
			case "period":
				_, err = s.DB().ExecContext(ctx, `UPDATE server_usage_calibrations SET period_start='2026-08-08' WHERE server_id=?`, current.ID)
			default:
				err = s.UpdateServerWithCalibration(ctx, &current, &amount, snapshot.PeriodStart, pair)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, beforeFound, err := s.GetServerUsageCalibration(ctx, current.ID, snapshot.PeriodStart)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := s.DisableServerIfQuotaUnchanged(ctx, snapshot, s.Now)
			got, getErr := s.GetServer(ctx, current.ID)
			after, afterFound, calErr := s.GetServerUsageCalibration(ctx, current.ID, snapshot.PeriodStart)
			if err != nil || changed || getErr != nil || !reflect.DeepEqual(got, current) || calErr != nil || beforeFound != afterFound || !reflect.DeepEqual(before, after) {
				t.Fatalf("stale stop changed calibration: changed=%v server=%+v cal=%+v err=%v/%v/%v", changed, got, after, err, getErr, calErr)
			}
		})
	}
}

func TestDisableServerQuotaRejectsObservationDayChange(t *testing.T) {
	for _, duringWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-write", true: "during-write"}[duringWrite], func(t *testing.T) {
			s, snapshot := serverQuotaFixture(t, true)
			calls := 0
			clock := func() time.Time {
				calls++
				if duringWrite && calls == 1 {
					return snapshot.ObservedAt
				}
				return snapshot.ObservedAt.AddDate(0, 0, 1)
			}
			changed, err := s.DisableServerIfQuotaUnchanged(context.Background(), snapshot, clock)
			got, getErr := s.GetServer(context.Background(), snapshot.Server.ID)
			if err != nil || changed || getErr != nil || !reflect.DeepEqual(got, snapshot.Server) {
				t.Fatalf("old-day decision committed: changed=%v got=%+v err=%v/%v", changed, got, err, getErr)
			}
		})
	}
}
