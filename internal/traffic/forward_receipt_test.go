package traffic_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

func TestForwardReceiptSettlesReleasesAndReplaysAtomically(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	server := domain.Server{Name: "receipt fixture", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = st.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	cfg := networkconfig.Forward{ListenMode: "all", ListenPort: 25001, Network: "tcp", TargetHost: "203.0.113.10", TargetPort: 443, SourceMode: "all", MaxTCPConnections: 2}
	op, err := st.RequestPortForward(ctx, store.PortForwardRequest{ID: strings.Repeat("a", 32), Action: "create", ServerID: server.ID, Name: "fixture", Enabled: true, Config: &cfg}, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	id := op.ResourceID
	ing := traffic.New(st)
	ing.Now = st.Now
	publish := func(key string) agentproto.ForwardReceipt {
		t.Helper()
		specs, err := st.DesiredForwards(ctx, server.ID)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(agentproto.DesiredState{ServerID: server.ID, NetworkBindingVersion: 1, NetworkForwardVersion: 1, Forwards: specs})
		ds, err := st.CreateDesiredState(ctx, server.ID, payload, strings.Repeat(key, 64))
		if err != nil {
			t.Fatal(err)
		}
		return agentproto.ForwardReceipt{ID: strings.Repeat(key, 32), Revision: ds.Revision, Hash: ds.Hash, TS: now, Counters: []agentproto.ForwardCounter{{ForwardID: id, Epoch: "fixture:forward:1", Rx: 100, Tx: 200}}}
	}
	update := func(key string, revision int64, port int) {
		t.Helper()
		cfg.ListenPort = port
		_, err := st.RequestPortForward(ctx, store.PortForwardRequest{ID: strings.Repeat(key, 32), Action: "update", ForwardID: id, ExpectedRevision: revision, Name: "fixture", Enabled: true, Config: &cfg}, domain.AuditEvent{})
		if err != nil {
			t.Fatal(err)
		}
	}
	accept := func(r agentproto.ForwardReceipt) {
		t.Helper()
		got, err := ing.Ingest(ctx, server, agentproto.Heartbeat{ForwardReceipt: &r})
		if err != nil || got.ForwardReceiptAck != r.ID {
			t.Fatal(got, err)
		}
	}
	ports := func(want ...int) {
		t.Helper()
		got, err := st.UsedListenPorts(ctx, server.ID)
		if err != nil || len(got) != len(want) {
			t.Fatal(got, err)
		}
		for _, p := range want {
			if !got[p] {
				t.Fatal(got)
			}
		}
	}
	first := publish("1")
	accept(first)
	update("b", 1, 25002)
	second := publish("2")
	update("c", 2, 25003)
	ports(25001, 25002, 25003)
	// A delayed receipt may retire only preceding ports, never a newer queued one.
	accept(second)
	ports(25002, 25003)
	accept(second)
	ports(25002, 25003)
	conflict := second
	conflict.Counters = append([]agentproto.ForwardCounter(nil), second.Counters...)
	conflict.Counters[0].Rx++
	if _, err = ing.Ingest(ctx, server, agentproto.Heartbeat{ForwardReceipt: &conflict}); err == nil {
		t.Fatal("conflicting receipt ID accepted")
	}
	_, err = st.RequestPortForward(ctx, store.PortForwardRequest{ID: strings.Repeat("d", 32), Action: "delete", ForwardID: id, ExpectedRevision: 3}, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	retired := publish("3")
	retired.Counters[0].Rx = 150
	retired.Counters[0].Tx = 250
	wrong := retired
	wrong.Hash = strings.Repeat("f", 64)
	if _, err = ing.Ingest(ctx, server, agentproto.Heartbeat{ForwardReceipt: &wrong}); err == nil {
		t.Fatal("wrong published hash accepted")
	}
	ports(25002, 25003)
	accept(retired)
	ports()
	if _, err = st.GetPortForward(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("retired resource remained", err)
	}
	accept(retired)
	accept(first)
	rx, tx, err := st.SumTraffic(ctx, store.SubjectForward, id, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || rx != 150 || tx != 250 {
		t.Fatal("lost or repeated final accounting", rx, tx, err)
	}
}
