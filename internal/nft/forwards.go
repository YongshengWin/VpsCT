package nft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentproto"
)

const ForwardTable = "ctlvps_forwards"

// ForwardRules preserves named counters across configuration changes. Removed
// marks are denied; counter deletion is a separate acknowledged cleanup step.
func ForwardRules(meters []agentproto.ForwardMeterRule) (string, error) {
	if len(meters) > 2*agentbudget.ActiveForwards {
		return "", errors.New("forward meter budget exceeded")
	}
	meters = append([]agentproto.ForwardMeterRule(nil), meters...)
	sort.Slice(meters, func(i, j int) bool { return meters[i].ForwardID < meters[j].ForwardID })
	ids, ports := map[int64]bool{}, map[int]bool{}
	var s strings.Builder
	fmt.Fprintf(&s, "add table inet %s\n", ForwardTable)
	for _, direction := range []string{"input", "output"} {
		fmt.Fprintf(&s, "add chain inet %s %s { type filter hook %s priority -140; policy accept; }\nflush chain inet %s %s\n", ForwardTable, direction, direction, ForwardTable, direction)
	}
	for _, m := range meters {
		mark, err := (agentproto.ResourceIdentity{Kind: "forward", ID: m.ForwardID}).Mark()
		if err != nil {
			return "", err
		}
		if ids[m.ForwardID] || (!m.Retired && (m.ListenPort < 1 || m.ListenPort > 65535 || ports[m.ListenPort])) || (m.Retired && !m.Blocked) {
			return "", errors.New("invalid forward meter identity")
		}
		ids[m.ForwardID] = true
		fmt.Fprintf(&s, "add counter inet %s f%d_rx\nadd counter inet %s f%d_tx\n", ForwardTable, m.ForwardID, ForwardTable, m.ForwardID)
		if m.Retired {
			continue
		}
		ports[m.ListenPort] = true
		fmt.Fprintf(&s, "add rule inet %s input iifname != \"lo\" ct direction original meta l4proto { tcp, udp } th dport %d ct mark set 0x%08x\n", ForwardTable, m.ListenPort, mark)
	}
	fmt.Fprintf(&s, "add rule inet %s output meta mark & 0xff000000 == 0x%08x ct mark set meta mark\n", ForwardTable, agentproto.ForwardMarkPrefix)
	fmt.Fprintf(&s, "add rule inet %s output meta mark & 0xff000000 == 0x%08x meta skuid != 0 drop\n", ForwardTable, agentproto.ForwardBootstrapMarkPrefix)
	fmt.Fprintf(&s, "add rule inet %s output meta mark & 0xff000000 == 0x%08x ct mark set meta mark\n", ForwardTable, agentproto.ForwardBootstrapMarkPrefix)
	for _, m := range meters {
		if m.Retired {
			continue
		}
		mark, _ := (agentproto.ResourceIdentity{Kind: "forward", ID: m.ForwardID}).Mark()
		bootstrap, _ := agentproto.ForwardBootstrapMark(m.ForwardID)
		for _, d := range []struct{ chain, iface, suffix string }{{"input", "iifname", "rx"}, {"output", "oifname", "tx"}} {
			if m.Blocked {
				fmt.Fprintf(&s, "add rule inet %s %s ct mark 0x%08x drop\n", ForwardTable, d.chain, bootstrap)
				fmt.Fprintf(&s, "add rule inet %s %s ct mark 0x%08x drop\n", ForwardTable, d.chain, mark)
				continue
			}
			fmt.Fprintf(&s, "add rule inet %s %s %s != \"lo\" ct mark 0x%08x counter name f%d_%s return\n", ForwardTable, d.chain, d.iface, bootstrap, m.ForwardID, d.suffix)
			fmt.Fprintf(&s, "add rule inet %s %s %s != \"lo\" ct mark 0x%08x counter name f%d_%s return\n", ForwardTable, d.chain, d.iface, mark, m.ForwardID, d.suffix)
		}
	}
	for _, direction := range []string{"input", "output"} {
		fmt.Fprintf(&s, "add rule inet %s %s ct mark & 0xff000000 == 0x%08x drop\n", ForwardTable, direction, agentproto.ForwardBootstrapMarkPrefix)
		fmt.Fprintf(&s, "add rule inet %s %s ct mark & 0xff000000 == 0x%08x drop\n", ForwardTable, direction, agentproto.ForwardMarkPrefix)
	}
	return s.String(), nil
}

func (m *Manager) EnsureForwards(ctx context.Context, meters []agentproto.ForwardMeterRule) error {
	if err := checkMarkPrefixes(ctx, []uint32{agentproto.ForwardMarkPrefix, agentproto.ForwardBootstrapMarkPrefix}); err != nil {
		return err
	}
	rules, err := ForwardRules(meters)
	if err != nil {
		return err
	}
	if _, err := m.run(ctx, rules, "--check", "-f", "-"); err != nil {
		return err
	}
	_, err = m.run(ctx, rules, "-f", "-")
	return err
}

func (m *Manager) ForwardsExist(ctx context.Context) bool {
	_, err := m.run(ctx, "", "list", "table", "inet", ForwardTable)
	return err == nil
}

func (m *Manager) ReadForwards(ctx context.Context) ([]agentproto.ForwardCounter, error) {
	raw, err := m.run(ctx, "", "-j", "list", "counters", "table", "inet", ForwardTable)
	if err != nil {
		return nil, err
	}
	return parseForwardCounters(raw)
}

// PruneForwards is called only after the frozen final snapshot has a durable
// controller acknowledgement. Repeated cleanup never resets retained meters.
func (m *Manager) PruneForwards(ctx context.Context, remaining []agentproto.ForwardMeterRule, retired []int64) error {
	rules, err := ForwardRules(remaining)
	if err != nil {
		return err
	}
	remove := map[int64]bool{}
	for _, id := range retired {
		if _, err := (agentproto.ResourceIdentity{Kind: "forward", ID: id}).Mark(); err != nil {
			return err
		}
		remove[id] = true
	}
	for _, item := range remaining {
		if remove[item.ForwardID] {
			return errors.New("cannot prune retained forward meter")
		}
	}
	readings, err := m.ReadForwards(ctx)
	if err != nil && m.ForwardsExist(ctx) {
		return err
	}
	for _, c := range readings {
		if remove[c.ForwardID] {
			rules += fmt.Sprintf("delete counter inet %s f%d_rx\ndelete counter inet %s f%d_tx\n", ForwardTable, c.ForwardID, ForwardTable, c.ForwardID)
		}
	}
	if _, err := m.run(ctx, rules, "--check", "-f", "-"); err != nil {
		return err
	}
	_, err = m.run(ctx, rules, "-f", "-")
	return err
}

func parseForwardCounters(raw []byte) ([]agentproto.ForwardCounter, error) {
	var doc struct {
		Nftables []struct {
			Counter *struct {
				Name           string
				Bytes, Packets int64
			}
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	values, seen := map[int64]agentproto.ForwardCounter{}, map[int64]int{}
	for _, entry := range doc.Nftables {
		if entry.Counter == nil {
			continue
		}
		c := entry.Counter
		var id int64
		var direction string
		if _, err := fmt.Sscanf(c.Name, "f%d_%s", &id, &direction); err != nil || (direction != "rx" && direction != "tx") || c.Name != fmt.Sprintf("f%d_%s", id, direction) {
			return nil, errors.New("unknown forward counter name")
		}
		v := values[id]
		v.ForwardID = id
		bit := 1
		if direction == "rx" {
			v.Rx, v.RxPkts = c.Bytes, c.Packets
		} else {
			bit = 2
			v.Tx, v.TxPkts = c.Bytes, c.Packets
		}
		if seen[id]&bit != 0 {
			return nil, errors.New("duplicate forward counter")
		}
		if err := v.ValidateValues(); err != nil {
			return nil, err
		}
		values[id], seen[id] = v, seen[id]|bit
		if len(values) > 2*agentbudget.ActiveForwards {
			return nil, errors.New("forward counter budget exceeded")
		}
	}
	out := make([]agentproto.ForwardCounter, 0, len(values))
	for id, v := range values {
		if seen[id] != 3 {
			return nil, errors.New("incomplete forward counter pair")
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ForwardID < out[j].ForwardID })
	return out, nil
}
