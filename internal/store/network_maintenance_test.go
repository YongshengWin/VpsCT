package store

import (
	"context"
	"errors"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/maintenance"
)

func TestMaintenanceParksNetworkPublicationWithoutLosingIntentOrBudget(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	op, err := s.RequestNodeNetwork(ctx, operationFixture(t, s), domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	job := MaintenanceJob{ServerID: op.ServerID, Job: maintenance.Job{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "update", Version: "v0.1.0"}, Status: "queued", CreatedAt: s.Now(), UpdatedAt: s.Now()}}
	if err = s.CreateMaintenance(ctx, job); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingNetworkPublications(ctx); err != nil || len(pending) != 0 {
		t.Fatal("worker selected a target reserved for maintenance", err)
	}
	if _, _, err = s.PublishDesired(ctx, 0, operationCandidate(op), true); !errors.Is(err, ErrNetworkMaintenance) {
		t.Fatal("in-flight publisher bypassed maintenance admission", err)
	}
	still, err := s.NetworkOperation(ctx, op.ID)
	if err != nil || still.Status != "queued" || still.Attempts != 0 {
		t.Fatal("maintenance consumed or discarded network intent", err)
	}
	job.Status = "failed"
	if err = s.SaveMaintenance(ctx, job, "queued"); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingNetworkPublications(ctx); err != nil || len(pending) != 1 || pending[0].Generation != op.Generation || pending[0].Attempts != 0 {
		t.Fatal("finished maintenance failed to release saved network intent", err)
	}
	if _, _, err = s.PublishDesired(ctx, 0, operationCandidate(op), false); err != nil {
		t.Fatal(err)
	}
}
