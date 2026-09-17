// Package nft manages per-port nftables counters as a fallback when systemd
// IPAccounting is not yet available. These only see the listen port (client
// ↔ VPS). Share billing prefers cgroup IPAccounting, which includes origin.
//
// Layout (table inet ctlvps):
//
//	counter in_<port>   client → VPS (listen dport)
//	counter out_<port>  VPS → client (listen sport)
package nft

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/boundedexec"
)

// Table name.
const Table = "ctlvps"

// Manager drives the nft binary.
type Manager struct {
	Bin string
}

// New returns a manager using `nft` from PATH.
func New() *Manager { return &Manager{Bin: "nft"} }

// Available reports whether nft can be executed.
func (m *Manager) Available(ctx context.Context) bool {
	return exec.CommandContext(ctx, m.Bin, "--version").Run() == nil
}

func (m *Manager) run(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	out, stderr, err := boundedexec.Run(ctx, stdin, 4<<20, m.Bin, args...)
	if err != nil {
		return nil, fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(stderr))
	}
	return out, nil
}

// Ensure converges the table to count exactly the given ports. Existing
// counters keep their values; counters of removed ports are deleted.
func (m *Manager) Ensure(ctx context.Context, ports []int) error {
	want := map[int]bool{}
	for _, p := range ports {
		if p > 0 && p < 65536 {
			want[p] = true
		}
	}
	sorted := make([]int, 0, len(want))
	for p := range want {
		sorted = append(sorted, p)
	}
	sort.Ints(sorted)

	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", Table)
	fmt.Fprintf(&b, "add chain inet %s input { type filter hook input priority -150; policy accept; }\n", Table)
	fmt.Fprintf(&b, "add chain inet %s output { type filter hook output priority -150; policy accept; }\n", Table)
	fmt.Fprintf(&b, "flush chain inet %s input\n", Table)
	fmt.Fprintf(&b, "flush chain inet %s output\n", Table)
	for _, p := range sorted {
		fmt.Fprintf(&b, "add counter inet %s in_%d\n", Table, p)
		fmt.Fprintf(&b, "add counter inet %s out_%d\n", Table, p)
	}
	for _, p := range sorted {
		fmt.Fprintf(&b, "add rule inet %s input tcp dport %d counter name \"in_%d\"\n", Table, p, p)
		fmt.Fprintf(&b, "add rule inet %s input udp dport %d counter name \"in_%d\"\n", Table, p, p)
		fmt.Fprintf(&b, "add rule inet %s output tcp sport %d counter name \"out_%d\"\n", Table, p, p)
		fmt.Fprintf(&b, "add rule inet %s output udp sport %d counter name \"out_%d\"\n", Table, p, p)
	}
	if _, err := m.run(ctx, b.String(), "-f", "-"); err != nil {
		return err
	}
	// drop counters of ports no longer wanted
	existing, err := m.Read(ctx)
	if err != nil {
		return nil // best effort
	}
	var del strings.Builder
	for p := range existing {
		if !want[p] {
			fmt.Fprintf(&del, "delete counter inet %s in_%d\ndelete counter inet %s out_%d\n", Table, p, Table, p)
		}
	}
	if del.Len() > 0 {
		_, _ = m.run(ctx, del.String(), "-f", "-")
	}
	for _, p := range sorted {
		_, _ = m.run(ctx, "", "delete", "counter", "inet", Table, fmt.Sprintf("orig_in_%d", p))
		_, _ = m.run(ctx, "", "delete", "counter", "inet", Table, fmt.Sprintf("orig_out_%d", p))
	}
	return nil
}

// Read returns cumulative counters per port.
func (m *Manager) Read(ctx context.Context) (map[int]agentproto.PortCounter, error) {
	out, err := m.run(ctx, "", "-j", "list", "counters", "table", "inet", Table)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("parse nft json: %w", err)
	}
	res := map[int]agentproto.PortCounter{}
	for _, item := range doc.Nftables {
		raw, ok := item["counter"]
		if !ok {
			continue
		}
		var c struct {
			Name    string `json:"name"`
			Packets int64  `json:"packets"`
			Bytes   int64  `json:"bytes"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			continue
		}
		port, inbound, ok := parseCounterName(c.Name)
		if !ok {
			continue
		}
		pc := res[port]
		pc.Port = port
		if inbound {
			pc.Rx += c.Bytes
			pc.RxPkts += c.Packets
		} else {
			pc.Tx += c.Bytes
			pc.TxPkts += c.Packets
		}
		res[port] = pc
	}
	return res, nil
}

func parseCounterName(name string) (port int, inbound bool, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(name, "orig_in_"):
		rest, inbound = strings.TrimPrefix(name, "orig_in_"), true
	case strings.HasPrefix(name, "orig_out_"):
		rest, inbound = strings.TrimPrefix(name, "orig_out_"), false
	case strings.HasPrefix(name, "in_"):
		rest, inbound = strings.TrimPrefix(name, "in_"), true
	case strings.HasPrefix(name, "out_"):
		rest, inbound = strings.TrimPrefix(name, "out_"), false
	default:
		return 0, false, false
	}
	port, err := strconv.Atoi(rest)
	if err != nil || port <= 0 {
		return 0, false, false
	}
	return port, inbound, true
}

// Teardown removes the table entirely.
func (m *Manager) Teardown(ctx context.Context) error {
	_, err := m.run(ctx, "", "delete", "table", "inet", Table)
	return err
}

// Exists reports whether the table is present.
func (m *Manager) Exists(ctx context.Context) bool {
	_, err := m.run(ctx, "", "list", "table", "inet", Table)
	return err == nil
}
