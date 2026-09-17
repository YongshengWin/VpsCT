package api

import (
	"context"
	"ctlvps/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPendingWorkerReportDoesNotClaimAppliedRevision(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	srv := domain.Server{Name: "worker-fixture", Enabled: true, CoreMode: domain.CoreModeStable}
	if err := c.api.Store.CreateServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	ag, err := c.api.Store.GetAgentByServer(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := c.api.Store.CreateDesiredState(ctx, srv.ID, []byte(`{}`), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.api.Store.MarkDesiredState(ctx, srv.ID, ds.Revision, domain.DesiredFailed, "old failure"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/agent/v1/apply-report", strings.NewReader(`{"revision":1,"hash":"fixture","status":"pending","error":"resource worker pending"}`))
	req = req.WithContext(context.WithValue(ctx, agentKey, &agentCtx{Agent: ag, Server: srv}))
	if err := c.api.agentApplyReport(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	ag, err = c.api.Store.GetAgentByServer(ctx, srv.ID)
	if err != nil || ag.AppliedRevision != 0 {
		t.Fatalf("pending revision marked applied: %+v %v", ag, err)
	}
	states, err := c.api.Store.ListDesiredStates(ctx, srv.ID, 1)
	if err != nil || len(states) != 1 || states[0].Status != domain.DesiredPending || states[0].AppliedAt != nil {
		t.Fatalf("pending state: %+v %v", states, err)
	}
}
