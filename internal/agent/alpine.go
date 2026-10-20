package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentproto"
)

// OpenRC has no systemd IPAccounting source. Never reinterpret an unsettled
// process meter as a listen-port nft meter: that would lose origin traffic.
func validateOpenRCState(s *State) error {
	if !s.MeteringV1 && len(s.MeterNodes) > 0 {
		return errors.New("OpenRC 无法结算旧 systemd 进程计量；请先在原 systemd 环境完成计量迁移和结算")
	}
	for _, n := range s.MeterNodes {
		if n.Core != "singbox" && !(n.Core == "mieru" && s.OpenRCMieruVersion == 1) {
			return fmt.Errorf("OpenRC 不支持节点 %d 的 %s 进程计量；请先在原 systemd 环境结算并移除该节点", n.NodeID, n.Core)
		}
	}
	if s.PendingSettlement != nil && hasProcessCounters(s.PendingSettlement.Ports) {
		return errors.New("OpenRC 存在未结算的旧 systemd 计量快照；请先在原 systemd 环境完成结算")
	}
	if p := s.Retirement; p != nil {
		if hasProcessCounters(p.Batch.Counters) {
			return errors.New("OpenRC 无法清理待结算的 systemd 计量；请先在原 systemd 环境完成节点退役")
		}
		for _, n := range p.Nodes {
			if n.Core != "singbox" && !(n.Core == "mieru" && s.OpenRCMieruVersion == 1) {
				return errors.New("OpenRC 无法清理待退役的非原生进程计量节点；请先在原 systemd 环境完成节点退役")
			}
		}
		for _, n := range p.Remaining {
			if n.Core != "singbox" && !(n.Core == "mieru" && s.OpenRCMieruVersion == 1) {
				return errors.New("OpenRC 不支持保留的非原生进程计量；请先在原 systemd 环境完成节点退役")
			}
		}
	}
	return nil
}

func hasProcessCounters(counters []agentproto.PortCounter) bool {
	for _, c := range counters {
		if c.Source == "" || strings.HasPrefix(c.Source, "systemd") {
			return true
		}
	}
	return false
}

func validateOpenRCDesired(ds *agentproto.DesiredState, s *State) error {
	if err := validateOpenRCState(s); err != nil {
		return err
	}
	for _, n := range ds.Nodes {
		if n.Core != "singbox" && n.Core != "mieru" {
			return fmt.Errorf("OpenRC 仅支持 sing-box 和 mieru 节点；节点 %d 的内核 %q 不支持", n.NodeID, n.Core)
		}
	}
	return nil
}

// Preserve the remote desired hash and serialized recovery cache. Only the
// tuning passed to the local drivers is reduced on a physically small Alpine.
func alpineProxyTuning(t agentproto.Tuning, memTotal int64) agentproto.Tuning {
	if memTotal <= 0 || memTotal > agentbudget.AlpineSmallMemoryBytes {
		return t
	}
	if t.MemoryMaxMB <= 0 || t.MemoryMaxMB > agentbudget.AlpineProxyMemoryMiB {
		t.MemoryMaxMB = agentbudget.AlpineProxyMemoryMiB
	}
	if t.GoMemLimitMB <= 0 || t.GoMemLimitMB > agentbudget.AlpineProxyGoMiB {
		t.GoMemLimitMB = agentbudget.AlpineProxyGoMiB
	}
	return t
}

func physicalMemoryTotal() int64 {
	// The kernel emits a small fixed-format file; no host facts are persisted.
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil || len(b) > 64<<10 {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemTotal:" && f[2] == "kB" {
			n, err := strconv.ParseInt(f[1], 10, 64)
			if err == nil && n > 0 && n <= (1<<63-1)/1024 {
				return n * 1024
			}
		}
	}
	return 0
}

func effectiveMemoryTotal() int64 {
	total := physicalMemoryTotal()
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || len(b) > 64<<10 {
		return total
	}
	for _, line := range strings.Split(string(b), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok && strings.HasPrefix(path, "/") && filepath.Clean(path) == path {
			// The agent's own 128 MiB safety ceiling does not describe VPS
			// capacity: proxy processes live in sibling cgroups. Inspect the
			// containing host/container limits outside our managed subtree.
			path = hostCapacityGroup(path)
			return cgroupMemoryTotal(total, "/sys/fs/cgroup", path)
		}
	}
	return total
}

func hostCapacityGroup(path string) string {
	const managed = "/ctlvps.slice/"
	if i := strings.Index(path, managed); i >= 0 {
		if i == 0 {
			return "/"
		}
		return path[:i]
	}
	return path
}

// cgroup limits constrain a container even though MemTotal describes its host.
// A parent can be tighter than the proxy's own group, so inspect all ancestors.
func cgroupMemoryTotal(total int64, root, group string) int64 {
	for depth := 0; depth < 256; depth++ {
		b, err := os.ReadFile(filepath.Join(root, group, "memory.max"))
		if err == nil && len(b) < 64 {
			limit, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			if err == nil && limit > 0 && (total <= 0 || limit < total) {
				total = limit
			}
		}
		if group == "/" {
			break
		}
		group = filepath.Dir(group)
	}
	return total
}

// A backend switch cannot silently replace a live BPF baseline with systemd
// counters, even though both implement the same service facade.
func validateSystemdMeterState(s *State) error {
	if s.OpenRCMieruVersion == 1 {
		for _, n := range s.MeterNodes {
			if n.Core == "mieru" {
				return errors.New("存在未结算的 OpenRC mieru 计量；请先在原 OpenRC 环境结算并退役节点")
			}
		}
	}
	if s.Retirement != nil {
		for _, c := range s.Retirement.Batch.Counters {
			if c.Source == "openrc-cgroup-v1" {
				return errors.New("存在待确认的 OpenRC 计量快照；请先在原环境完成结算")
			}
		}
	}
	if s.PendingSettlement != nil {
		for _, c := range s.PendingSettlement.Ports {
			if c.Source == "openrc-cgroup-v1" {
				return errors.New("存在待确认的 OpenRC 计量快照；请先在原环境完成结算")
			}
		}
	}
	return nil
}
