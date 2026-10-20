package store

import (
	"context"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func TestServerQuotaModeMigrationPreservesPreviouslyEffectiveBilling(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	srv := domain.Server{Name: "legacy", QuotaResetDay: 8, Enabled: true}
	if err := s.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTraffic(ctx, SubjectServer, srv.ID, now, 100, 50); err != nil {
		t.Fatal(err)
	}
	target := int64(200)
	if err := s.UpdateServerWithCalibration(ctx, &srv, &target, time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	// Simulate a v31 row: the old code retained user-selected strings but
	// always billed inbound+outbound and saved a scalar observed total.
	if _, err := s.db.Exec(`UPDATE servers SET quota_billing='max' WHERE id=?;
		ALTER TABLE server_usage_calibrations DROP COLUMN observed_up_bytes;
		ALTER TABLE server_usage_calibrations DROP COLUMN observed_down_bytes;
		UPDATE schema_version SET version=31`, srv.ID); err != nil {
		t.Fatal(err)
	}
	// Exercise the v32 data migration itself; newer schema objects already
	// exist in this fixture and must not be replayed by rewinding its marker.
	if _, err := s.db.Exec(migrations[31]); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServer(ctx, srv.ID)
	if err != nil || got.QuotaBilling != domain.BillingDual {
		t.Fatalf("legacy quota mode changed effective billing: %+v %v", got, err)
	}
	calibration, ok, err := s.GetServerUsageCalibration(ctx, srv.ID, time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC))
	if err != nil || !ok || calibration.ObservedBytes != 150 || calibration.HasDirectionalSnapshot {
		t.Fatalf("legacy calibration changed: %+v %v %v", calibration, ok, err)
	}
}
