package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentproto"
)

func TestOpenRCRejectsUnsupportedDesiredBeforeRememberingMeters(t *testing.T) {
	for _, kind := range []string{"snell", "other", ""} {
		t.Run(kind, func(t *testing.T) {
			s := &State{}
			ds := &agentproto.DesiredState{Nodes: []agentproto.NodeSpec{{NodeID: 4, Core: kind, Blocked: true}}}
			if err := validateOpenRCDesired(ds, s); err == nil || !strings.Contains(err.Error(), "仅支持 sing-box") {
				t.Fatalf("unsupported core accepted: %v", err)
			}
			if len(s.MeterNodes) != 0 || s.MeteringV1 {
				t.Fatal("rejected configuration mutated accounting state")
			}
		})
	}
	if err := validateOpenRCDesired(&agentproto.DesiredState{Nodes: []agentproto.NodeSpec{{NodeID: 1, Core: "singbox"}}}, &State{}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRCRejectsEveryUnsettledSystemdAccountingPath(t *testing.T) {
	for _, s := range []*State{
		{MeterNodes: []MeterIdentity{{Core: "singbox"}}},
		{MeteringV1: true, MeterNodes: []MeterIdentity{{Core: "snell"}}},
		{MeteringV1: true, MeterNodes: []MeterIdentity{{Core: "mieru"}}},
		{PendingSettlement: &agentproto.Heartbeat{Ports: []agentproto.PortCounter{{Port: 10001}}}},
		{PendingSettlement: &agentproto.Heartbeat{Ports: []agentproto.PortCounter{{Source: "systemd-legacy-v1"}}}},
		{Retirement: &Retirement{Batch: agentproto.MeterSettlement{Counters: []agentproto.PortCounter{{Source: "systemd-v1"}}}}},
		{Retirement: &Retirement{Nodes: []MeterIdentity{{Core: "snell"}}}},
		{Retirement: &Retirement{Remaining: []MeterRule{{Core: "mieru"}}}},
	} {
		if err := validateOpenRCState(s); err == nil {
			t.Fatalf("unsupported meter was accepted: %+v", s)
		}
	}
	// A sing-box nft settlement remains valid on OpenRC; its counters must
	// neither be relabeled nor combined with process-accounted counters.
	s := &State{MeteringV1: true, MeterNodes: []MeterIdentity{{Core: "singbox"}}, Retirement: &Retirement{
		Nodes: []MeterIdentity{{Core: "singbox"}}, Remaining: []MeterRule{{Core: "singbox"}},
		Batch: agentproto.MeterSettlement{Counters: []agentproto.PortCounter{{Source: "nft-node-v1"}}},
	}}
	if err := validateOpenRCState(s); err != nil {
		t.Fatal(err)
	}
}

func TestAlpineBudgetRespectsTightestCgroupAncestor(t *testing.T) {
	for path, want := range map[string]string{
		"/ctlvps.slice/ctlvps-agent.service":        "/",
		"/tenant/ctlvps.slice/ctlvps-agent.service": "/tenant",
		"/tenant/plain-service":                     "/tenant/plain-service",
	} {
		if got := hostCapacityGroup(path); got != want {
			t.Fatalf("agent safety ceiling confused with host capacity: %s => %s", path, got)
		}
	}
	root := t.TempDir()
	for _, entry := range []struct{ path, value string }{
		{"memory.max", "536870912"}, {"tenant/memory.max", "268435456"}, {"tenant/agent/memory.max", "max"},
	} {
		path := filepath.Join(root, entry.path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(entry.value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := cgroupMemoryTotal(8<<30, root, "/tenant/agent"); got != 256<<20 {
		t.Fatalf("container used host RAM instead of ancestor limit: %d", got)
	}
	if got := cgroupMemoryTotal(128<<20, root, "/tenant/agent"); got != 128<<20 {
		t.Fatalf("cgroup limit raised physical memory: %d", got)
	}
	if got := cgroupMemoryTotal(0, root, "/tenant/agent"); got != 256<<20 {
		t.Fatalf("known cgroup limit lost when physical sample unavailable: %d", got)
	}
	if err := os.WriteFile(filepath.Join(root, "tenant", "agent", "memory.max"), []byte("100663296"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := cgroupMemoryTotal(8<<30, root, "/tenant/agent"); got != 96<<20 {
		t.Fatalf("leaf memory cap was ignored: %d", got)
	}
}

func TestAlpineSmallPhysicalHostClampsOnlyProxyMemoryDefaults(t *testing.T) {
	original := agentproto.Tuning{MemoryMaxMB: 256, GoMemLimitMB: 192, EnableBBR: true, Chrony: true, LimitNOFILE: 8192, RestartSec: 4}
	for _, total := range []int64{128 << 20, 256 << 20, agentbudget.AlpineSmallMemoryBytes} {
		got := alpineProxyTuning(original, total)
		if got.MemoryMaxMB != 96 || got.GoMemLimitMB != 64 || got.EnableBBR != original.EnableBBR || got.Chrony != original.Chrony || got.LimitNOFILE != original.LimitNOFILE || got.RestartSec != original.RestartSec {
			t.Fatalf("bad local clamp: %+v", got)
		}
	}
	for _, total := range []int64{0, -1, 512 << 20, agentbudget.AlpineSmallMemoryBytes + 1} {
		if got := alpineProxyTuning(original, total); got != original {
			t.Fatalf("larger/unknown host was changed: %+v", got)
		}
	}
	lower := agentproto.Tuning{MemoryMaxMB: 80, GoMemLimitMB: 32}
	if got := alpineProxyTuning(lower, 256<<20); got != lower {
		t.Fatalf("local clamp raised an explicit lower limit: %+v", got)
	}
	if got := alpineProxyTuning(agentproto.Tuning{}, 256<<20); got.MemoryMaxMB != 96 || got.GoMemLimitMB != 64 {
		t.Fatalf("zero defaults bypassed small-memory budget: %+v", got)
	}
	if original.MemoryMaxMB != 256 || original.GoMemLimitMB != 192 {
		t.Fatal("remote tuning was modified")
	}
}

func TestOpenRCMieruKeepsNativeMetersButRejectsImportedSystemdState(t *testing.T) {
	s := &State{MeteringV1: true, OpenRCVersion: 1, OpenRCMieruVersion: 1, MeterNodes: []MeterIdentity{{NodeID: 1, Core: "mieru"}}, Retirement: &Retirement{Nodes: []MeterIdentity{{Core: "mieru"}}, Remaining: []MeterRule{{Core: "mieru"}}, Batch: agentproto.MeterSettlement{Counters: []agentproto.PortCounter{{Source: "openrc-cgroup-v1"}}}}}
	if err := validateOpenRCState(s); err != nil {
		t.Fatal(err)
	}
	if err := validateOpenRCDesired(&agentproto.DesiredState{Nodes: []agentproto.NodeSpec{{Core: "mieru"}}}, &State{}); err != nil {
		t.Fatal(err)
	}
	s.Retirement.Batch.Counters[0].Source = "systemd-v1"
	if err := validateOpenRCState(s); err == nil {
		t.Fatal("accepted unsettled systemd counters")
	}
}

func TestOpenRCMieruMustSettleBeforeSwitchingToSystemd(t *testing.T) {
	s := &State{OpenRCMieruVersion: 1, MeterNodes: []MeterIdentity{{Core: "mieru"}}}
	if err := validateSystemdMeterState(s); err == nil {
		t.Fatal("accepted live BPF baseline on systemd")
	}
	s.MeterNodes = nil
	if err := validateSystemdMeterState(s); err != nil {
		t.Fatal(err)
	}
	s.Retirement = &Retirement{Batch: agentproto.MeterSettlement{Counters: []agentproto.PortCounter{{Source: "openrc-cgroup-v1"}}}}
	if err := validateSystemdMeterState(s); err == nil {
		t.Fatal("accepted unsettled BPF snapshot on systemd")
	}
}
