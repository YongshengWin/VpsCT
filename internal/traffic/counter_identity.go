package traffic

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
)

func addTrafficDirections(inbound, outbound, addInbound, addOutbound int64) (int64, int64, error) {
	if !domain.ValidTrafficTotals(inbound, outbound) || !domain.ValidTrafficTotals(addInbound, addOutbound) || addInbound > math.MaxInt64-inbound || addOutbound > math.MaxInt64-outbound {
		return 0, 0, errors.New("traffic totals overflow or are invalid")
	}
	inbound, outbound = inbound+addInbound, outbound+addOutbound
	if !domain.ValidTrafficTotals(inbound, outbound) {
		return 0, 0, errors.New("traffic totals overflow")
	}
	return inbound, outbound, nil
}

// Released Linux agents use the kernel boot UUID followed by the nft nonce.
// Unknown legacy epochs may identify an agent restart instead of a NIC reset.
func linuxBootEpoch(epoch string) string {
	boot, nonce, ok := strings.Cut(epoch, ":")
	if !ok || nonce == "" || len(boot) != 36 || boot[8] != '-' || boot[13] != '-' || boot[18] != '-' || boot[23] != '-' {
		return ""
	}
	if raw, err := hex.DecodeString(strings.ReplaceAll(boot, "-", "")); err != nil || len(raw) != 16 {
		return ""
	}
	return strings.ToLower(boot)
}

// A node has one live meter. The released accounting migration deliberately
// sends the same frozen legacy reading twice: its port receipt settles the old
// baseline, while the identified receipt only establishes the new baseline.
func nodeCounterBaselines(counters []agentproto.PortCounter, byID map[int64]domain.Node, byPort map[int]domain.Node) (map[int64]bool, error) {
	byNode := map[int64][]agentproto.PortCounter{}
	for _, counter := range counters {
		node, known := byPort[counter.Port]
		if counter.NodeID > 0 {
			node, known = byID[counter.NodeID]
		}
		if known {
			byNode[node.ID] = append(byNode[node.ID], counter)
		}
	}
	baselineOnly := map[int64]bool{}
	for id, readings := range byNode {
		if len(readings) < 2 {
			continue
		}
		if len(readings) != 2 {
			return nil, errors.New("overlapping node meter sources")
		}
		legacy, identified := readings[0], readings[1]
		if identified.NodeID == 0 {
			legacy, identified = identified, legacy
		}
		if legacy.NodeID != 0 || legacy.Source != "" || identified.NodeID != id || identified.Source != "systemd-legacy-v1" || identified.FromZero || legacy.Port != identified.Port || legacy.Rx != identified.Rx || legacy.Tx != identified.Tx {
			return nil, errors.New("overlapping node meter sources")
		}
		baselineOnly[id] = true
	}
	return baselineOnly, nil
}
