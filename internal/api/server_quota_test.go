package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

func serverQuotaTestAPI(t *testing.T) (*API, domain.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	tr := traffic.New(st)
	tr.Now = st.Now
	a := &API{Deps: Deps{Store: st, Traffic: tr, Desired: desired.New(st)}}
	v := domain.Server{Name: "quota-fixture", QuotaBytes: 100, QuotaResetDay: 8, QuotaBilling: domain.BillingMax, Enabled: true}
	ctx := context.Background()
	if err := st.CreateServer(ctx, &v); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTraffic(ctx, store.SubjectServer, v.ID, now, 150, 50); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "quota.action", "stop"); err != nil {
		t.Fatal(err)
	}
	return a, v
}

func assertServerQuotaNoStop(t *testing.T, a *API, id int64) {
	t.Helper()
	got, err := a.Store.GetServer(context.Background(), id)
	var auditCount int
	auditErr := a.Store.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='server.auto_disable'`).Scan(&auditCount)
	if err != nil || !got.Enabled || auditErr != nil || auditCount != 0 {
		t.Fatalf("stale decision stopped server: %+v audit=%d err=%v/%v", got, auditCount, err, auditErr)
	}
}

func TestServerQuotaReReadsPolicyBeforeEvaluating(t *testing.T) {
	a, stale := serverQuotaTestAPI(t)
	ctx := context.Background()
	current := stale
	current.Name, current.QuotaBytes, current.QuotaBilling = "admin edited", 1000, domain.BillingDown
	amount := int64(20)
	period := traffic.PeriodStart(a.Traffic.Now(), current.QuotaResetDay)
	if err := a.Store.UpdateServerWithCalibration(ctx, &current, &amount, period); err != nil {
		t.Fatal(err)
	}
	a.checkServerQuota(ctx, stale)
	assertServerQuotaNoStop(t, a, stale.ID)
	got, _ := a.Store.GetServer(ctx, stale.ID)
	cal, found, err := a.Store.GetServerUsageCalibration(ctx, got.ID, period)
	if got.Name != current.Name || got.QuotaBytes != current.QuotaBytes || got.QuotaBilling != current.QuotaBilling || err != nil || !found || cal.CalibratedBytes != amount {
		t.Fatalf("stale authenticated record overwrote settings/calibration: %+v %+v %v", got, cal, err)
	}
}

func TestServerQuotaRejectsConcurrentPolicyAndCalibrationChanges(t *testing.T) {
	for _, change := range []string{"policy", "calibration", "directional-baseline"} {
		t.Run(change, func(t *testing.T) {
			a, v := serverQuotaTestAPI(t)
			ctx := context.Background()
			now := a.Store.Now()
			period := traffic.PeriodStart(now, v.QuotaResetDay)
			amount := int64(160)
			if change == "directional-baseline" {
				if err := a.Store.UpdateServerWithCalibration(ctx, &v, &amount, period, [2]int64{160, 80}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			a.Traffic.Now = func() time.Time {
				calls++
				// Second call is ServerUsage's clock; third follows usage reads.
				trigger := 2
				if change == "directional-baseline" {
					trigger = 3
				}
				if calls == trigger {
					var err error
					switch change {
					case "policy":
						v.Name, v.QuotaBytes = "admin edited", 1000
						err = a.Store.UpdateServer(ctx, &v)
					case "calibration":
						amount = 20
						err = a.Store.UpdateServerWithCalibration(ctx, &v, &amount, period)
					case "directional-baseline":
						if err = a.Store.AddTraffic(ctx, store.SubjectServer, v.ID, now, 0, 1); err == nil {
							err = a.Store.UpdateServerWithCalibration(ctx, &v, &amount, period, [2]int64{160, 80})
						}
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				return now
			}
			a.checkServerQuota(ctx, v)
			assertServerQuotaNoStop(t, a, v.ID)
		})
	}
}

func TestServerQuotaUsesTrafficClockAtWriteAndAuditsOnlySuccess(t *testing.T) {
	a, v := serverQuotaTestAPI(t)
	ctx := context.Background()
	now := a.Store.Now()
	calls := 0
	a.Traffic.Now = func() time.Time {
		calls++
		if calls >= 4 {
			return now.AddDate(0, 0, 1)
		}
		return now
	}
	a.checkServerQuota(ctx, v)
	assertServerQuotaNoStop(t, a, v.ID)
	a.Traffic.Now = func() time.Time { return now }
	a.checkServerQuota(ctx, v)
	a.checkServerQuota(ctx, v)
	got, err := a.Store.GetServer(ctx, v.ID)
	var auditCount int
	auditErr := a.Store.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='server.auto_disable'`).Scan(&auditCount)
	if err != nil || got.Enabled || auditErr != nil || auditCount != 1 {
		t.Fatalf("successful stop was not committed once: %+v audit=%d err=%v/%v", got, auditCount, err, auditErr)
	}
}
