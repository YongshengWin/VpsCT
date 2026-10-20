package store

import (
	"context"
	"testing"

	"ctlvps/internal/domain"
)

func TestTrafficFormulaMigrationPreservesEffectiveShareBilling(t *testing.T) {
	ctx := context.Background()
	old, path := historicalStore(t, 33)
	srv := domain.Server{Name: "fixture", Enabled: true, QuotaBilling: domain.BillingMax}
	historicalCreateServer(t, old, &srv)
	for _, mode := range []string{"dual", "sum", "up", "down", "max"} {
		sh := domain.Share{Name: mode, BillingMode: mode, UsedUpload: 100, UsedDownload: 50}
		if err := old.CreateShare(ctx, &sh); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()
	current, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	shares, err := current.ListShares(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sh := range shares {
		if sh.BillingMode != domain.BillingDual || sh.UsedUpload != 100 || sh.UsedDownload != 50 {
			t.Fatalf("old ignored mode reinterpreted or ledger changed: %+v", sh)
		}
	}
	got, err := current.GetServer(ctx, srv.ID)
	if err != nil || got.QuotaBilling != domain.BillingMax {
		t.Fatal("migration changed active server mode", got, err)
	}
}
