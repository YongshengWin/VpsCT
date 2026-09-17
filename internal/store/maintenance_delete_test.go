package store

import (
	"context"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/maintenance"
)

func TestUninstallDeletionRollsBackWithReceipt(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	srv := &domain.Server{Name: "atomic-delete", Enabled: true}
	if err := s.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	j := MaintenanceJob{Job: maintenance.Job{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "uninstall"}, Status: "running", CreatedAt: s.Now(), UpdatedAt: s.Now()}, ServerID: srv.ID, DeleteServer: true, ReportToken: "scoped-test-token"}
	if err := s.CreateMaintenance(ctx, j); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_delete BEFORE DELETE ON servers BEGIN SELECT RAISE(ABORT,'simulated failure'); END`); err != nil {
		t.Fatal(err)
	}
	j.Status = "succeeded"
	if err := s.SaveMaintenance(ctx, j, "running"); err == nil {
		t.Fatal("expected deletion failure")
	}
	saved, err := s.GetMaintenance(ctx, j.ID)
	if err != nil || saved.Status != "running" || saved.ReportToken == "" {
		t.Fatalf("receipt incorrectly committed: %+v %v", saved, err)
	}
	if _, err := s.GetServer(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER reject_delete`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMaintenance(ctx, j, "running"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetServer(ctx, srv.ID); err != ErrNotFound {
		t.Fatalf("server retained: %v", err)
	}
	saved, err = s.GetMaintenance(ctx, j.ID)
	if err != nil || saved.Status != "succeeded" || saved.ReportToken != "" || saved.ReportHash == "" {
		t.Fatalf("receipt not retained safely: %+v %v", saved, err)
	}
}
