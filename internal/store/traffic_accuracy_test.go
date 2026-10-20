package store

import (
	"context"
	"math"
	"testing"
	"time"

	"ctlvps/internal/domain"
)

func TestTrafficEntriesAndWindowTotalsRejectOverflow(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return at.Add(24 * time.Hour) }
	half := int64(math.MaxInt64 / 2)
	if err := s.AddTraffic(ctx, SubjectNode, 1, at, half, 0); err != nil {
		t.Fatal(err)
	}
	// The new hour alone fits, but the day's directional sum does not. Both
	// bucket changes must roll back rather than creating a SQLite REAL value.
	if err := s.AddTraffic(ctx, SubjectNode, 1, at.Add(time.Hour), 0, half+2); err == nil {
		t.Fatal("daily total overflow accepted")
	}
	hours, err := s.HourlyTraffic(ctx, SubjectNode, 1, at)
	if err != nil || len(hours) != 1 || hours[0].Up != half || hours[0].Down != 0 {
		t.Fatal("failed daily update left its new hourly bucket", err)
	}
	if err := s.AddTraffic(ctx, SubjectNode, 1, at, -1, 0); err == nil {
		t.Fatal("negative delta accepted")
	}
	if err := s.SetDailyTraffic(ctx, SubjectExternal, 1, at, half+1, half+1); err == nil {
		t.Fatal("invalid external directional total accepted")
	}
	if err := s.AddSample(ctx, domain.TrafficSample{ServerID: 1, TS: at, RxBytes: -1}); err == nil {
		t.Fatal("negative cumulative sample accepted")
	}
	if err := s.AddTraffic(ctx, SubjectNode, 1, at.Add(24*time.Hour), 0, half+2); err != nil {
		t.Fatal(err)
	}
	// Each day is representable, while the combined window is not.
	if _, _, err := s.SumTraffic(ctx, SubjectNode, 1, at, at.Add(24*time.Hour)); err == nil {
		t.Fatal("window total overflow accepted")
	}
	if _, _, err := s.SumTrafficIDs(ctx, SubjectNode, []int64{1}, at, at.Add(24*time.Hour)); err == nil {
		t.Fatal("multi-ID window total overflow accepted")
	}
	if _, err := s.NodeTrafficSummaries(ctx, 2); err == nil {
		t.Fatal("node summary published an overflowing total")
	}
	if err := s.AddTraffic(ctx, SubjectNode, 2, at, 0, half+2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DailyTotals(ctx, SubjectNode, at, at); err == nil {
		t.Fatal("dashboard aggregation published an overflowing total")
	}
}
