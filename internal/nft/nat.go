package nft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
)

// NATTable belongs exclusively to the agent. Port translation happens before
// node accounting, so core listeners and their billing identities keep using
// the public port selected by the controller.
const NATTable = "ctlvps_nat"

func natNodeNetworks(n agentproto.NodeSpec) ([]string, error) {
	return natconfig.ProtocolNetworks(n.Protocol, n.Params)
}

// NATRules uses validated, already established provider mappings selected by
// the agent's local/controller configuration authority.
// Unallocated mappings do not capture traffic, and identity mappings need no
// translation. A port-only DNAT preserves the destination address, including
// nodes that explicitly bind one of the VPS's local IPv4 addresses.
func NATRules(cfg natconfig.Config, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) (string, error) {
	pairs, err := cfg.Expand()
	if err != nil {
		return "", err
	}
	if len(pairs) == 0 {
		return "", nil
	}
	listeners := map[int]bool{}
	active := map[int]map[string]bool{}
	for _, n := range nodes {
		if n.Retired {
			continue
		}
		if n.ListenPort < 1 || n.ListenPort > 65535 {
			return "", errors.New("NAT 节点监听端口无效")
		}
		if listeners[n.ListenPort] {
			return "", errors.New("NAT 资源监听端口重复")
		}
		listeners[n.ListenPort] = true
		if n.Blocked {
			continue
		}
		networks, err := natNodeNetworks(n)
		if err != nil {
			return "", err
		}
		if active[n.ListenPort] != nil {
			return "", errors.New("NAT 节点监听端口重复")
		}
		active[n.ListenPort] = map[string]bool{}
		for _, network := range networks {
			active[n.ListenPort][network] = true
		}
	}
	for _, f := range forwards {
		if f.Retired {
			continue
		}
		if f.Config.ListenPort < 1 || f.Config.ListenPort > 65535 {
			return "", errors.New("NAT 检查发现固定转发监听端口无效")
		}
		if listeners[f.Config.ListenPort] {
			return "", errors.New("NAT 资源监听端口重复")
		}
		listeners[f.Config.ListenPort] = true
		if f.Blocked {
			continue
		}
		var networks []string
		switch f.Config.Network {
		case "tcp", "udp":
			networks = []string{f.Config.Network}
		case "both":
			networks = []string{"tcp", "udp"}
		default:
			return "", errors.New("NAT 固定转发的入站传输无效")
		}
		active[f.Config.ListenPort] = map[string]bool{}
		for _, network := range networks {
			active[f.Config.ListenPort][network] = true
		}
	}
	var selected []natconfig.PortMapping
	for _, p := range pairs {
		if p.LocalPort == p.PublicPort {
			continue
		}
		// Check every configured source, even an unused mapping, against all
		// current resources. A blocked resource remains reserved until retired.
		if listeners[p.LocalPort] {
			return "", fmt.Errorf("NAT 本地端口 %d 与节点或固定转发监听端口冲突", p.LocalPort)
		}
		if active[p.PublicPort][p.Network] {
			selected = append(selected, p)
		}
	}
	if len(selected) == 0 {
		return "", nil
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].PublicPort != selected[j].PublicPort {
			return selected[i].PublicPort < selected[j].PublicPort
		}
		if selected[i].LocalPort != selected[j].LocalPort {
			return selected[i].LocalPort < selected[j].LocalPort
		}
		return selected[i].Network < selected[j].Network
	})
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", NATTable)
	fmt.Fprintf(&b, "add chain inet %s prerouting { type nat hook prerouting priority dstnat; policy accept; }\n", NATTable)
	fmt.Fprintf(&b, "flush chain inet %s prerouting\n", NATTable)
	for _, p := range selected {
		fmt.Fprintf(&b, "add rule inet %s prerouting meta nfproto ipv4 iifname != \"lo\" fib daddr type local", NATTable)
		if cfg.Interface != "" {
			fmt.Fprintf(&b, " iifname %q", cfg.Interface)
		}
		if cfg.LocalAddress != "" {
			fmt.Fprintf(&b, " ip daddr %s", cfg.LocalAddress)
		}
		fmt.Fprintf(&b, " %s dport %d dnat to :%d comment %q\n", p.Network, p.LocalPort, p.PublicPort, fmt.Sprintf("ctlvps-nat:%s:%d", p.Network, p.PublicPort))
	}
	return b.String(), nil
}

// natTableExists relies on a successful table inventory. Failed access to nft
// must never masquerade as a missing table and a successful disabled setup.
func (m *Manager) natTableExists(ctx context.Context) (bool, error) {
	raw, err := m.run(ctx, "", "-j", "list", "tables")
	if err != nil {
		return false, err
	}
	var doc struct {
		Nftables json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Nftables) == 0 || string(doc.Nftables) == "null" {
		return false, errors.New("无法确认 NAT 表状态：nftables 表清单无效")
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(doc.Nftables, &entries); err != nil {
		return false, errors.New("无法确认 NAT 表状态：nftables 表清单无效")
	}
	for _, entry := range entries {
		value, ok := entry["table"]
		if !ok {
			continue
		}
		var table struct {
			Family string `json:"family"`
			Name   string `json:"name"`
		}
		if err := json.Unmarshal(value, &table); err != nil || table.Family == "" || table.Name == "" {
			return false, errors.New("无法确认 NAT 表状态：nftables 表记录无效")
		}
		if table.Family == "inet" && table.Name == NATTable {
			return true, nil
		}
	}
	return false, nil
}

// EnsureNAT modifies only our table. Both activation and removal are preflighted
// and applied as one transaction, without flushing any administrator rules.
func (m *Manager) EnsureNAT(ctx context.Context, cfg natconfig.Config, nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec) error {
	rules, err := NATRules(cfg, nodes, forwards)
	if err != nil {
		return err
	}
	if rules == "" {
		exists, err := m.natTableExists(ctx)
		if err != nil || !exists {
			return err
		}
		rules = "delete table inet " + NATTable + "\n"
	}
	if _, err := m.run(ctx, rules, "--check", "-f", "-"); err != nil {
		return err
	}
	_, err = m.run(ctx, rules, "-f", "-")
	return err
}
