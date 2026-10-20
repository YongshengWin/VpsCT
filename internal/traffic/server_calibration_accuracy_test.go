package traffic_test

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

func TestServerCalibrationOverflowDoesNotInventUsage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode       string
		directions bool
	}{
		{"scalar-dual", domain.BillingDual, false},
		{"scalar-max", domain.BillingMax, false},
		{"directions-max", domain.BillingMax, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			srv := domain.Server{Name: "overflow-fixture", QuotaResetDay: 8, QuotaBilling: tc.mode, Enabled: true}
			if err = st.CreateServer(ctx, &srv); err != nil {
				t.Fatal(err)
			}
			target := int64(math.MaxInt64 - 1)
			var directions [][2]int64
			if tc.directions {
				directions = append(directions, [2]int64{target, 0})
			}
			if err = st.UpdateServerWithCalibration(ctx, &srv, &target, traffic.PeriodStart(now, srv.QuotaResetDay), directions...); err != nil {
				t.Fatal(err)
			}
			if err = st.AddTraffic(ctx, store.SubjectServer, srv.ID, now, 3, 0); err != nil {
				t.Fatal(err)
			}
			if usage, err := ing.ServerUsage(ctx, srv); err == nil {
				t.Fatalf("unrepresentable calibrated usage must not be clamped: %+v", usage)
			}
			up, down, err := st.SumTraffic(ctx, store.SubjectServer, srv.ID, traffic.PeriodStart(now, srv.QuotaResetDay), now)
			if err != nil || up != 3 || down != 0 {
				t.Fatalf("usage evaluation must preserve raw ledger: %d/%d %v", up, down, err)
			}
		})
	}
}
