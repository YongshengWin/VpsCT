package nft

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/boundedexec"
)

const ingressComment = "ctlvps-node-ingress"

type ingressEntry struct {
	Family  string                       `json:"family"`
	Table   string                       `json:"table"`
	Name    string                       `json:"name"`
	Chain   string                       `json:"chain"`
	Hook    string                       `json:"hook"`
	Policy  string                       `json:"policy"`
	Comment string                       `json:"comment"`
	Handle  uint64                       `json:"handle"`
	Expr    []map[string]json.RawMessage `json:"expr"`
}

var errForeignIngress = errors.New("检测到其他入站防火墙规则，请手动放行节点端口；自动开放仅支持原生 inet filter input")

// IngressRules modifies only our tagged rules in the administrator's input
// chain. A separate accept base chain cannot override an existing drop chain.
// Other firewall managers are deliberately not rewritten.

func IngressRules(snapshot []byte, nodes []agentproto.NodeSpec) (string, error) {
	return ingressRules(snapshot, nodes, nil)
}

func ingressRules(snapshot []byte, nodes []agentproto.NodeSpec, compat map[string]string) (string, error) {
	var doc struct {
		Nftables []struct {
			Chain *ingressEntry `json:"chain"`
			Rule  *ingressEntry `json:"rule"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(snapshot, &doc); err != nil {
		return "", err
	}
	ports := map[string]map[int]bool{"tcp": {}, "udp": {}}
	for _, n := range nodes {
		if n.Blocked || n.Retired {
			continue
		}
		if n.ListenPort < 1 || n.ListenPort > 65535 {
			return "", fmt.Errorf("节点监听端口无效")
		}
		switch n.Protocol {
		case "hysteria2", "tuic":
			ports["udp"][n.ListenPort] = true
		case "ss", "shadowsocks":
			ports["tcp"][n.ListenPort] = true
			ports["udp"][n.ListenPort] = true
		case "vless", "vmess", "trojan", "anytls", "snell":
			ports["tcp"][n.ListenPort] = true
		default:
			return "", fmt.Errorf("无法确定节点协议 %q 的入站端口", n.Protocol)
		}
	}
	var owned []*ingressEntry
	target := false
	foreignSeen, ownedAfterForeign := false, false
	for _, e := range doc.Nftables {
		if c := e.Chain; c != nil && c.Hook == "input" {
			if c.Family == "inet" && c.Table == "filter" && c.Name == "input" {
				target = true
				continue
			}
			if c.Table == "ctlvps" || c.Table == NodeTable || c.Table == "ctlvps_egress" {
				continue
			}
			// Empty permissive compatibility chains are harmless. Never claim to
			// override another manager's policy or its jumps/rejects.
			constrained := c.Policy != "accept"
			var chainRules []*ingressEntry
			for _, r := range doc.Nftables {
				if r.Rule != nil && r.Rule.Family == c.Family && r.Rule.Table == c.Table && r.Rule.Chain == c.Name {
					constrained = true
					chainRules = append(chainRules, r.Rule)
				}
			}
			if constrained && (len(ports["tcp"])+len(ports["udp"]) > 0) && !compatInputDisjoint(c, chainRules, ports, compat[c.Family]) {
				return "", errForeignIngress
			}
		}
		if r := e.Rule; r != nil && r.Family == "inet" && r.Table == "filter" && r.Chain == "input" {
			if strings.HasPrefix(r.Comment, ingressComment+":") {
				owned = append(owned, r)
				ownedAfterForeign = ownedAfterForeign || foreignSeen
			} else {
				foreignSeen = true
			}
		}
	}
	if !target {
		return "", nil
	} // No restrictive native input chain to open.
	var commands []string
	for _, proto := range []string{"tcp", "udp"} {
		var ps []int
		for p := range ports[proto] {
			ps = append(ps, p)
		}
		sort.Ints(ps)
		var values []string
		for _, p := range ps {
			values = append(values, strconv.Itoa(p))
		}
		if len(values) > 0 {
			commands = append(commands, fmt.Sprintf("insert rule inet filter input %s dport { %s } accept comment %q\n", proto, strings.Join(values, ", "), fmt.Sprintf("%s:%s:%x", ingressComment, proto, sha256.Sum256([]byte(strings.Join(values, ","))))))
		}
	}
	// Avoid rewriting rules every heartbeat when both port sets are unchanged.
	matching := len(owned) == len(commands) && !ownedAfterForeign
	seen := map[string]bool{}
	for _, r := range owned {
		found := false
		for _, cmd := range commands {
			if strings.Contains(cmd, strconv.Quote(r.Comment)) {
				found = true
			}
		}
		if !found || seen[r.Comment] {
			matching = false
		}
		seen[r.Comment] = true
	}
	if matching {
		return "", nil
	}
	var out strings.Builder
	for _, r := range owned {
		fmt.Fprintf(&out, "delete rule inet filter input handle %d\n", r.Handle)
	}
	for _, cmd := range commands {
		out.WriteString(cmd)
	}
	return out.String(), nil
}

func (m *Manager) EnsureIngress(ctx context.Context, nodes []agentproto.NodeSpec) error {
	snapshot, err := m.run(ctx, "", "-j", "list", "ruleset")
	if err != nil {
		return err
	}
	script, err := IngressRules(snapshot, nodes)
	if errors.Is(err, errForeignIngress) {
		compat := map[string]string{}
		for family, bin := range map[string]string{"ip": "iptables", "ip6": "ip6tables"} {
			version, _, e := boundedexec.Run(ctx, "", 4096, bin, "--version")
			if e != nil || !strings.Contains(string(version), "(nf_tables)") {
				continue // A legacy backend is a different firewall, not corroborating evidence.
			}
			out, _, e := boundedexec.Run(ctx, "", 1<<20, bin, "-S", "INPUT")
			if e == nil {
				compat[family] = string(out)
			}
		}
		script, err = ingressRules(snapshot, nodes, compat)
	}
	if err != nil || script == "" {
		return err
	}
	if _, err = m.run(ctx, script, "--check", "-f", "-"); err != nil {
		return err
	}
	_, err = m.run(ctx, script, "-f", "-")
	return err
}
