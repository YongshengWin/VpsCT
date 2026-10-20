package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/provision"
)

func TestDelayedSuccessCannotMarkNewerResourceFailureApplied(t *testing.T) {
	c := newTestAPI(t)
	ctx := context.Background()
	sv := domain.Server{Name: "receipt-fixture", PublicHost: "fixture.example.test", Enabled: true, CoreMode: domain.CoreModeStable}
	if err := c.api.Store.CreateServer(ctx, &sv); err != nil {
		t.Fatal(err)
	}
	n, err := provision.NewNode(sv, "", provision.Options{Protocol: "ss", Port: 21001})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.api.Store.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	rec, _, err := c.api.Desired.Publish(ctx, sv.ID)
	if err != nil {
		t.Fatal(err)
	}
	ag, _ := c.api.Store.GetAgentByServer(ctx, sv.ID)
	ctx = context.WithValue(ctx, agentKey, &agentCtx{Agent: ag, Server: sv})
	send := func(rep agentproto.ApplyReport) {
		t.Helper()
		raw, _ := json.Marshal(rep)
		r := httptest.NewRequest("POST", agentproto.PathApplyReport, strings.NewReader(string(raw))).WithContext(ctx)
		w := httptest.NewRecorder()
		if err := c.api.agentApplyReport(w, r); err != nil || w.Code != 204 {
			t.Fatal("application receipt rejected", err, w.Code)
		}
	}
	rep := agentproto.ApplyReport{Revision: rec.Revision, Hash: rec.Hash, Status: "applied", NodeApplicationSequence: 1, NodeApplications: []agentproto.NodeApplication{{NodeID: n.ID, Core: "singbox", Status: "applied"}}}
	send(rep)
	failure := rep
	failure.Status, failure.Error, failure.NodeApplicationSequence = "failed", "guard fixture failure", 2
	failure.NodeApplications = []agentproto.NodeApplication{{NodeID: n.ID, Core: "singbox", Status: "failed"}}
	send(failure)
	send(rep)
	rep.NodeApplicationSequence, rep.NodeApplications = 0, nil
	send(rep) // An older executable's whole-configuration acknowledgment.
	hb := agentproto.Heartbeat{Epoch: "fixture", AppliedRevision: rec.Revision, AppliedHash: rec.Hash, ApplyStatus: "applied"}
	raw, _ := json.Marshal(hb)
	r := httptest.NewRequest("POST", agentproto.PathHeartbeat, strings.NewReader(string(raw))).WithContext(ctx)
	if err := c.api.agentHeartbeat(httptest.NewRecorder(), r); err != nil {
		t.Fatal("stale heartbeat could not continue observation", err)
	}
	states, err := c.api.Store.ListDesiredStates(ctx, sv.ID, 1)
	if err != nil || len(states) != 1 || states[0].Status != domain.DesiredFailed {
		t.Fatal("delayed report changed failed desired state to applied", err)
	}
	ag, err = c.api.Store.GetAgentByServer(ctx, sv.ID)
	if err != nil || ag.ApplyError == "" {
		t.Fatal("delayed report cleared current failure", err)
	}
	if _, usable, err := c.api.Store.AppliedSubscriptionNode(ctx, n); err != nil || usable {
		t.Fatal("delayed report reopened failed subscription node", err)
	}
}
