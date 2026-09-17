package agent

import (
	"context"
	"ctlvps/internal/agentproto"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type retiredFixture struct {
	fail    bool
	cleaned int
}

func (f *retiredFixture) FinalRead(_ context.Context, nodes []MeterIdentity) ([]agentproto.PortCounter, error) {
	var out []agentproto.PortCounter
	for _, n := range nodes {
		out = append(out, agentproto.PortCounter{NodeID: n.NodeID, Source: "nft-node-v1", Epoch: "fixture:" + n.Generation, Rx: 100, Tx: 200, FromZero: true})
	}
	return out, nil
}
func (f *retiredFixture) Clean(_ context.Context, p *Retirement) error {
	if !p.Acked {
		return errors.New("cleanup before ACK")
	}
	if f.fail {
		return errors.New("fixture cleanup failure")
	}
	f.cleaned += len(p.Nodes)
	return nil
}

func TestRetirementCrashAndLostAck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	host := &retiredFixture{}
	attempts := 0
	fail := true
	firstID := ""
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hb agentproto.Heartbeat
		json.NewDecoder(r.Body).Decode(&hb)
		attempts++
		if firstID == "" {
			firstID = hb.FinalMeters.ID
		} else if firstID != hb.FinalMeters.ID {
			t.Error("retry changed immutable batch")
		}
		if fail {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(agentproto.HeartbeatResponse{FinalMeterVersion: 1, FinalMeterAck: hb.FinalMeters.ID})
	}))
	defer srv.Close()
	st := &State{ServerURL: srv.URL, AgentToken: "fixture", MeteringV1: true, MeterNodes: []MeterIdentity{{NodeID: 1, Core: "singbox"}}}
	a := New(dir, st, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	a.Client.HTTP = srv.Client()
	a.finalMeters = true
	a.desired = &agentproto.DesiredState{}
	a.retirementHost = host
	if err := a.beginRetirement(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.flushRetirement(ctx); err == nil || host.cleaned != 0 {
		t.Fatal("ACK loss cleaned state")
	}
	// Recreate the coordinator from disk at each persistence boundary.
	reload := func() {
		var err error
		st, err = LoadState(dir)
		if err != nil {
			t.Fatal(err)
		}
		a = New(dir, st, slog.Default(), "test")
		a.Client.HTTP = srv.Client()
		a.retirementHost = host
	}
	reload()
	fail = false
	host.fail = true
	if err := a.flushRetirement(ctx); err == nil {
		t.Fatal("expected cleanup failure")
	}
	reload()
	if !a.State.Retirement.Acked {
		t.Fatal("ACK not durable")
	}
	host.fail = false
	if err := a.flushRetirement(ctx); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(a.State.MeterNodes) != 0 || a.State.Retirement != nil {
		t.Fatal("cleanup replay or state growth")
	}
	reload()
	if a.State.Retirement != nil || len(a.State.MeterNodes) != 0 {
		t.Fatal("completed retirement resurrected")
	}
}

func TestRetirementChurnDoesNotAccumulateHistory(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hb agentproto.Heartbeat
		json.NewDecoder(r.Body).Decode(&hb)
		json.NewEncoder(w).Encode(agentproto.HeartbeatResponse{FinalMeterVersion: 1, FinalMeterAck: hb.FinalMeters.ID})
	}))
	defer srv.Close()
	a := New(t.TempDir(), &State{ServerURL: srv.URL, AgentToken: "fixture", MeteringV1: true}, slog.Default(), "test")
	a.Client.HTTP = srv.Client()
	a.finalMeters = true
	host := &retiredFixture{}
	a.retirementHost = host
	previousGeneration := ""
	for round := 0; round < 300; round++ {
		// Reuse the same two logical IDs, each time after its previous generation retired.
		id := int64(round%2 + 1)
		nodes := []agentproto.NodeSpec{{NodeID: id, Core: "singbox", ListenPort: 10000 + int(id)}}
		if err := a.State.rememberMeters(nodes); err != nil {
			t.Fatal(err)
		}
		a.desired = &agentproto.DesiredState{Nodes: nodes}
		if err := a.beginRetirement(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := a.flushRetirement(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(a.State.MeterNodes) != 1 {
			t.Fatalf("round %d retained %d", round, len(a.State.MeterNodes))
		}
		generation := a.State.MeterNodes[0].Generation
		if generation == "" || generation == previousGeneration {
			t.Fatal("generation reused")
		}
		previousGeneration = generation
	}
	if host.cleaned != 299 {
		t.Fatal(host.cleaned)
	}
}
