package agent

import (
	"context"
	"ctlvps/internal/agentproto"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNetworkBillingDurableRetry(t *testing.T) {
	var requests []agentproto.Heartbeat
	fail := true
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var h agentproto.Heartbeat
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			t.Error(err)
		}
		requests = append(requests, h)
		if fail {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(agentproto.HeartbeatResponse{NetworkBillingVersion: 1, NetworkBillingAck: h.NetworkBillingSwitch.ID})
	}))
	defer server.Close()
	dir := t.TempDir()
	id := strings.Repeat("a", 32)
	sw := &agentproto.NetworkBillingSwitch{ID: strings.Repeat("b", 32), Next: agentproto.NetworkBillingPolicy{Revision: 1, Mode: "interfaces", InterfaceIDs: []string{id}}, TS: time.Now().UTC(), Legacy: agentproto.LegacyNetworkCounters{Epoch: "boot", Rx: 50, Tx: 60}, Snapshot: &agentproto.NetworkSnapshot{Version: 1, CollectorID: strings.Repeat("c", 32), BootID: "boot", Sequence: 1, SampledAt: time.Now().UTC(), Status: "ok", Interfaces: []agentproto.NetworkInterface{{ID: id, Generation: strings.Repeat("d", 32), Name: "eth0", Index: 2, Kind: "physical", CountersValid: true, Rx: 100, Tx: 200}}}}
	st := &State{ServerURL: server.URL, AgentToken: "test-fixture", NetworkBillingPending: sw}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	a := New(dir, st, slog.Default(), "fixture")
	a.Client.HTTP = server.Client()
	if err := a.flushNetworkBilling(context.Background()); err == nil {
		t.Fatal("expected uncertain ACK")
	}
	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NetworkBillingPolicy != nil || loaded.NetworkBillingPending == nil {
		t.Fatal("policy advanced before ACK")
	}
	a = New(dir, loaded, slog.Default(), "fixture")
	a.Client.HTTP = server.Client()
	fail = false
	if err = a.flushNetworkBilling(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatal("restart changed frozen billing snapshot")
	}
	if requests[0].Metrics.NetRx != 0 || requests[0].Metrics.NetTx != 0 || len(requests[0].Ports) != 0 {
		t.Fatal("settlement can be mistaken for legacy counters")
	}
	loaded, err = LoadState(dir)
	if err != nil || loaded.NetworkBillingPending != nil || loaded.NetworkBillingPolicy.Revision != 1 {
		t.Fatal("ACK was not committed", err)
	}
}
