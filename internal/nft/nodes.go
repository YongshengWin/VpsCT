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
)

// NodeTable is separate from legacy listen-port counters. Marks are reserved
// for VpsCT and never restored to packet marks (which could affect routing).
const NodeTable = "ctlvps_nodes"
const MarkMask = agentproto.NodeMarkMask
const MarkPrefix = agentproto.NodeMarkPrefix

func NodeMark(id int64) (uint32, error) {
	return agentproto.NodeMark(id)
}

// NodeRules is one atomic nft transaction. Named counters survive every rule
// update and retired counters remain readable for final settlement.
func NodeRules(nodes []agentproto.NodeSpec) (string, error) {
	nodes = append([]agentproto.NodeSpec(nil), nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", NodeTable)
	fmt.Fprintf(&b, "add chain inet %s input { type filter hook input priority -140; policy accept; }\n", NodeTable)
	fmt.Fprintf(&b, "add chain inet %s output { type filter hook output priority -140; policy accept; }\n", NodeTable)
	fmt.Fprintf(&b, "flush chain inet %s input\nflush chain inet %s output\n", NodeTable, NodeTable)
	// Client connections acquire a node identity on input; origin connections
	// acquire it from SO_MARK on output. Conntrack carries it in both directions.
	ports, ids := map[int]bool{}, map[int64]bool{}
	for _, n := range nodes {
		if n.Core != "singbox" {
			continue
		}
		mark, err := NodeMark(n.NodeID)
		if err != nil {
			return "", err
		}
		if (!n.Retired && (n.ListenPort < 1 || n.ListenPort > 65535 || ports[n.ListenPort])) || ids[n.NodeID] {
			return "", fmt.Errorf("duplicate/invalid accounting identity")
		}
		ids[n.NodeID] = true
		if !n.Retired {
			ports[n.ListenPort] = true
		}
		fmt.Fprintf(&b, "add counter inet %s n%d_rx\nadd counter inet %s n%d_tx\n", NodeTable, n.NodeID, NodeTable, n.NodeID)
		if n.Retired {
			continue
		}
		fmt.Fprintf(&b, "add rule inet %s input iifname != \"lo\" ct direction original meta l4proto { tcp, udp } th dport %d ct mark & 0x%08x != 0x%08x ct mark set 0x%08x\n", NodeTable, n.ListenPort, MarkMask, MarkPrefix, mark)
	}
	fmt.Fprintf(&b, "add rule inet %s output oifname != \"lo\" meta mark & 0x%08x == 0x%08x ct mark set meta mark\n", NodeTable, MarkMask, MarkPrefix)
	// Bootstrap sockets are opened by root for an unprivileged DNS parser.
	// Proxy-owned sockets may not use this namespace to bypass a pending lease.
	fmt.Fprintf(&b, "add rule inet %s output meta mark & 0x%08x == 0x%08x meta skuid != 0 drop\n", NodeTable, MarkMask, agentproto.BootstrapMarkPrefix)
	fmt.Fprintf(&b, "add rule inet %s output oifname != \"lo\" meta mark & 0x%08x == 0x%08x ct mark set meta mark\n", NodeTable, MarkMask, agentproto.BootstrapMarkPrefix)
	for _, n := range nodes {
		if n.Core != "singbox" || n.Retired {
			continue
		}
		mark, _ := NodeMark(n.NodeID)
		action := " return"
		if n.Blocked {
			action = " drop"
		}
		fmt.Fprintf(&b, "add rule inet %s input iifname != \"lo\" ct mark 0x%08x counter name n%d_rx%s\n", NodeTable, mark, n.NodeID, action)
		if n.Blocked {
			fmt.Fprintf(&b, "add rule inet %s output ct mark 0x%08x drop\n", NodeTable, mark)
		}
		fmt.Fprintf(&b, "add rule inet %s output oifname != \"lo\" ct mark 0x%08x counter name n%d_tx return\n", NodeTable, mark, n.NodeID)
		if n.Network != nil && n.Network.HasTransport() {
			bootstrap, _ := agentproto.BootstrapMark(n.NodeID)
			fmt.Fprintf(&b, "add rule inet %s input iifname != \"lo\" ct mark 0x%08x counter name n%d_rx%s\n", NodeTable, bootstrap, n.NodeID, action)
			fmt.Fprintf(&b, "add rule inet %s output oifname != \"lo\" ct mark 0x%08x counter name n%d_tx%s\n", NodeTable, bootstrap, n.NodeID, action)
		}
	}
	// Unknown/retired marks cannot escape once their per-node rules disappear.
	// These two constant rules replace an ever-growing set of retired drop rules.
	fmt.Fprintf(&b, "add rule inet %s input iifname != \"lo\" ct mark & 0x%08x == 0x%08x drop\n", NodeTable, MarkMask, MarkPrefix)
	fmt.Fprintf(&b, "add rule inet %s output oifname != \"lo\" ct mark & 0x%08x == 0x%08x drop\n", NodeTable, MarkMask, MarkPrefix)
	for _, chain := range []string{"input", "output"} {
		fmt.Fprintf(&b, "add rule inet %s %s ct mark & 0x%08x == 0x%08x drop\n", NodeTable, chain, MarkMask, agentproto.BootstrapMarkPrefix)
	}
	return b.String(), nil
}

func (m *Manager) EnsureNodes(ctx context.Context, nodes []agentproto.NodeSpec) error {
	if err := checkMarkRoutes(ctx); err != nil {
		return err
	}
	rules, err := NodeRules(nodes)
	if err != nil {
		return err
	}
	if _, err = m.run(ctx, rules, "--check", "-f", "-"); err != nil {
		return err
	}
	_, err = m.run(ctx, rules, "-f", "-")
	return err
}

func (m *Manager) ReadNodes(ctx context.Context) ([]agentproto.PortCounter, error) {
	out, err := m.run(ctx, "", "-j", "list", "counters", "table", "inet", NodeTable)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Nftables []struct {
			Counter *struct {
				Name    string
				Bytes   int64
				Packets int64
			}
		}
	}
	if err = json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	counters := map[int64]*agentproto.PortCounter{}
	seen := map[int64]int{}
	for _, entry := range doc.Nftables {
		if entry.Counter == nil {
			continue
		}
		c := entry.Counter
		var id int64
		var dir string
		if _, err := fmt.Sscanf(c.Name, "n%d_%s", &id, &dir); err != nil || id <= 0 || (dir != "rx" && dir != "tx") {
			continue
		}
		p := counters[id]
		if p == nil {
			p = &agentproto.PortCounter{NodeID: id, Source: "nft-node-v1", FromZero: true}
			counters[id] = p
		}
		if dir == "rx" {
			p.Rx, p.RxPkts = c.Bytes, c.Packets
			seen[id] |= 1
		} else {
			p.Tx, p.TxPkts = c.Bytes, c.Packets
			seen[id] |= 2
		}
	}
	outCounters := make([]agentproto.PortCounter, 0, len(counters))
	for id, c := range counters {
		if seen[id] != 3 {
			return nil, fmt.Errorf("incomplete node counter %d", id)
		}
		outCounters = append(outCounters, *c)
	}
	sort.Slice(outCounters, func(i, j int) bool { return outCounters[i].NodeID < outCounters[j].NodeID })
	return outCounters, nil
}

func (m *Manager) NodesExist(ctx context.Context) bool {
	_, err := m.run(ctx, "", "list", "table", "inet", NodeTable)
	return err == nil
}

// A packet mark must not accidentally select an existing policy-routing table.
func checkMarkRoutes(ctx context.Context) error {
	return checkMarkPrefixes(ctx, []uint32{MarkPrefix, agentproto.BootstrapMarkPrefix})
}

func checkMarkPrefixes(ctx context.Context, prefixes []uint32) error {
	for _, family := range []string{"-4", "-6"} {
		raw, err := exec.CommandContext(ctx, "ip", family, "-j", "rule", "show").Output()
		if err != nil {
			return fmt.Errorf("inspect routing marks: %w", err)
		}
		var rules []map[string]any
		if err = json.Unmarshal(raw, &rules); err != nil {
			return err
		}
		number := func(v any) (uint32, error) { n, e := strconv.ParseUint(fmt.Sprint(v), 0, 32); return uint32(n), e }
		for _, r := range rules {
			v, ok := r["fwmark"]
			if !ok {
				continue
			}
			mark, e := number(v)
			if e != nil {
				return fmt.Errorf("unrecognized policy routing mark")
			}
			mask := uint32(0xffffffff)
			if v, ok := r["fwmask"]; ok {
				mask, e = number(v)
				if e != nil {
					return e
				}
			}
			for _, prefix := range prefixes {
				if (mark^prefix)&mask&MarkMask == 0 {
					return fmt.Errorf("policy routing overlaps VpsCT accounting marks; existing routing left unchanged")
				}
			}
		}
	}
	return nil
}

// PruneNodes atomically removes references before deleting acknowledged
// counters. Replaying after a crash preserves all active counters.
func (m *Manager) PruneNodes(ctx context.Context, remaining []agentproto.NodeSpec, ids []int64) error {
	rules, err := NodeRules(remaining)
	if err != nil {
		return err
	}
	existing, err := m.ReadNodes(ctx)
	if err != nil {
		// After a host reboot the kernel table is gone. Rebuild the retained
		// rules; acknowledged old counters have already disappeared with it.
		if m.NodesExist(ctx) {
			return err
		}
	}
	wanted := map[int64]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	for _, n := range remaining {
		if wanted[n.NodeID] {
			return fmt.Errorf("cannot prune a retained meter")
		}
	}
	var b strings.Builder
	b.WriteString(rules)
	for _, c := range existing {
		if wanted[c.NodeID] {
			fmt.Fprintf(&b, "delete counter inet %s n%d_rx\ndelete counter inet %s n%d_tx\n", NodeTable, c.NodeID, NodeTable, c.NodeID)
		}
	}
	if _, err = m.run(ctx, b.String(), "--check", "-f", "-"); err != nil {
		return err
	}
	_, err = m.run(ctx, b.String(), "-f", "-")
	return err
}
