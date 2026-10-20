package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/nft"
)

func TestNATListenerInspectionDoesNotTreatOriginTCPAsListener(t *testing.T) {
	raw := []byte("  sl  local_address rem_address   st\n   0: 00000000:2711 00000000:0000 0A\n   1: 0100007F:2712 0100007F:01BB 01\n")
	ports, err := natListenerPorts(raw, "tcp")
	if err != nil || !ports[10001] || ports[10002] {
		t.Fatalf("TCP listeners: %v %v", ports, err)
	}
	ports, err = natListenerPorts(raw, "udp")
	if err != nil || !ports[10001] || !ports[10002] {
		t.Fatalf("UDP bindings: %v %v", ports, err)
	}
	for _, raw := range []string{"", "bad header", "local_address\nshort", "local_address\n0: addr:zz 0:0 0A"} {
		if _, err := natListenerPorts([]byte(raw), "tcp"); err == nil {
			t.Fatal("unreadable socket inventory accepted")
		}
	}
}

func TestNATPartialSuccessSurvivesFailedSiblingHeartbeat(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "nft")
	log := filepath.Join(dir, "calls")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf called >> '"+log+"'\nprintf '%s' '{\"nftables\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{natReady: true, NFT: &nft.Manager{Bin: bin}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StateDir: dir, State: &State{NATVersion: 1, ServerURL: "https://fixture.example", AgentToken: "fixture", ApplyError: "snell installation failed", NodeApplicationRevision: 4, NodeApplicationHash: strings.Repeat("a", 64), NodeApplicationSequence: 8, NodeApplications: []agentproto.NodeApplication{{NodeID: 1, Core: "singbox", Status: "applied"}, {NodeID: 2, Core: "snell", Status: "failed"}}}}
	cfg := natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001}}}
	a.natAppliedKey = natChoiceKey(natChoice(cfg, "local", nil))
	a.refreshNATForDesired(context.Background(), nil, cfg, nil)
	if _, err := os.Stat(log); !os.IsNotExist(err) || a.State.NodeApplications[0].Status != "applied" || a.State.NodeApplicationSequence != 8 {
		t.Fatal("failed sibling erased independently verified NAT or receipts")
	}
	// Explicit local disable must still remove mappings and withdraw receipts.
	a.refreshNATForDesired(context.Background(), nil, natconfig.Config{Schema: 1}, nil)
	if _, err := os.Stat(log); err != nil || a.State.NodeApplications[0].Status != "failed" || a.State.NodeApplicationSequence != 9 {
		t.Fatal("local disable retained the partial NAT success")
	}
}

func TestNATEarlyApplyFailureCannotRetainPreviousSuccess(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "nft")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' '{\"nftables\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Agent{NFT: &nft.Manager{Bin: bin}, StateDir: dir, State: &State{NATVersion: 1, ServerURL: "https://fixture.example", AgentToken: "fixture", NodeApplicationRevision: 4, NodeApplicationHash: strings.Repeat("a", 64), NodeApplicationSequence: 8, NodeApplications: []agentproto.NodeApplication{{NodeID: 1, Core: "singbox", Status: "applied"}}}}
	for i := int64(1); i <= maxMeterIdentities; i++ {
		a.State.MeterNodes = append(a.State.MeterNodes, MeterIdentity{NodeID: i})
	}
	if _, err := a.apply(context.Background(), &agentproto.DesiredState{Nodes: []agentproto.NodeSpec{{NodeID: maxMeterIdentities + 1}}}); err == nil {
		t.Fatal("exhausted meter admission should fail before starting a core")
	}
	st, err := LoadState(dir)
	if err != nil || st.NodeApplicationSequence != 9 || st.NodeApplications[0].Status != "failed" {
		t.Fatalf("old success survived NAT cleanup on early error: %v %v", st, err)
	}
}

func TestNATLocalFailureWithdrawsLastSuccessWithNewSequence(t *testing.T) {
	a := &Agent{StateDir: t.TempDir(), State: &State{ServerURL: "https://fixture.example", AgentToken: "fixture", NodeApplicationRevision: 4, NodeApplicationHash: strings.Repeat("a", 64), NodeApplicationSequence: 8, NodeApplications: []agentproto.NodeApplication{{NodeID: 1, Core: "singbox", Status: "applied"}}}}
	if err := a.invalidateNATApplications(); err != nil {
		t.Fatal(err)
	}
	s, err := LoadState(a.StateDir)
	if err != nil || s.NodeApplicationSequence != 9 || s.NodeApplications[0].Status != "failed" || s.NodeApplicationRevision != 4 {
		t.Fatalf("failure must durably supersede the last applied receipt: %+v %v", s, err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	a.StateDir = blocked
	a.State.NodeApplications[0].Status = "applied"
	if err := a.invalidateNATApplications(); err == nil || len(a.State.NodeApplications) != 0 || a.State.NodeApplicationRevision != 0 {
		t.Fatal("an undurable failure receipt left an old success in memory")
	}
}
