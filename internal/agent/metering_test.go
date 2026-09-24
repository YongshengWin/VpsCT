package agent

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMigrationSettlementRetry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("METER_FIXTURE", dir)
	fake := `#!/bin/sh
case "$1" in
 is-active) if [ "$2" = ctlvps-singbox@21001.service ] && [ ! -f "$METER_FIXTURE/stopped" ]; then echo active; exit 0; fi; echo inactive; exit 3;;
 stop) touch "$METER_FIXTURE/stopped";;
 start) rm -f "$METER_FIXTURE/stopped";;
 show) if [ ! -f "$METER_FIXTURE/stopped" ]; then echo 'sample before stop' >&2; exit 1; fi
 printf 'Id=ctlvps-singbox@21001.service\nInvocationID=fixture-invocation\nIPIngressBytes=150\nIPEgressBytes=270\n';;
 list-units) echo 'ctlvps-singbox@21001.service loaded active running fixture';;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := []agentproto.Heartbeat{}
	fail := true
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hb agentproto.Heartbeat
		if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
			t.Error(err)
		}
		calls = append(calls, hb)
		if fail {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(agentproto.HeartbeatResponse{MeteringVersion: 1})
	}))
	defer server.Close()
	state := &State{ServerURL: server.URL, AgentToken: "isolated-fixture", CounterNonce: "fixture"}
	a := New(dir, state, slog.Default(), "fixture")
	a.Client.HTTP = server.Client()
	ds := &agentproto.DesiredState{Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", ListenPort: 21001}}}
	if _, err := a.prepareMetering(context.Background(), ds); err == nil {
		t.Fatal("expected failed ACK")
	}
	if _, err := os.Stat(filepath.Join(dir, "stopped")); !os.IsNotExist(err) {
		t.Fatal("old service not restored")
	}
	persisted, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.PendingSettlement == nil || persisted.LegacySettled {
		t.Fatal("missing durable pending settlement")
	}
	fail = false
	a = New(dir, persisted, slog.Default(), "fixture")
	a.Client.HTTP = server.Client()
	if err = a.flushSettlement(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[0], calls[1]) {
		t.Fatal("retry changed the settlement payload")
	}
	if len(calls[0].Ports) != 2 || calls[0].Ports[1].FromZero {
		t.Fatal("migration must establish modern baseline without recounting history")
	}
	persisted, err = LoadState(dir)
	if err != nil || !persisted.LegacySettled || persisted.PendingSettlement != nil {
		t.Fatal("ACK not committed", err)
	}
	restore, err := a.prepareMetering(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	restore(true)
	ports := calls[len(calls)-1].Ports
	if len(ports) != 1 || !ports[0].FromZero || !strings.Contains(ports[0].Source, "legacy") {
		t.Fatal("retry after rollback reuses legacy billing baseline")
	}
	if _, err = os.Stat(filepath.Join(dir, "stopped")); err != nil {
		t.Fatal("successful cutover restarted legacy service")
	}
	// Binding fences rely on modern per-node marks. A failed migration must
	// not restart unmarked legacy services beneath a newly requested binding.
	if err = os.Remove(filepath.Join(dir, "stopped")); err != nil {
		t.Fatal(err)
	}
	ds.Nodes[0].Network = &agentproto.NodeNetworkSpec{Policy: networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}}
	restore, err = a.prepareMetering(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	restore(false)
	if _, err = os.Stat(filepath.Join(dir, "stopped")); err != nil {
		t.Fatal("failed guarded migration revived an unmarked legacy process")
	}
}
