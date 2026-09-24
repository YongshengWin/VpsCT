package store

import (
	"context"
	"encoding/json"
	"sort"

	"ctlvps/internal/domain"
)

type NetworkHistoricalNode struct {
	NodeID     int64  `json:"node_id"`
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	ListenPort int    `json:"listen_port"`
	ShareID    *int64 `json:"share_id,omitempty"`
}

type NetworkHistoricalForward struct {
	ForwardID  int64 `json:"forward_id"`
	Revision   int64 `json:"revision"`
	ListenPort int   `json:"listen_port"`
}

// A failed multi-core apply can have activated a newer sing-box configuration
// while retaining the last fully applied revision. Include every retained
// publication since that revision, not only the latest or successful payload.
// Missing/pruned history is explicit uncertainty, never an empty live scope.
func (s *Store) addImpactHistory(ctx context.Context, q querier, v *NetworkImpact) error {
	v.HistoryComplete = true
	v.HistoricalNodes = []NetworkHistoricalNode{}
	v.HistoricalForwards = nil
	if !v.RuntimeChange || v.RestartScope != "server_singbox" {
		return nil
	}
	var applied int64
	var appliedHash string
	if err := q.QueryRowContext(ctx, `SELECT applied_revision,applied_hash FROM agents WHERE server_id=?`, v.ServerID).Scan(&applied, &appliedHash); err != nil {
		return err
	}
	const maxRevisions = 64
	const maxBytes = 16 << 20
	rows, err := q.QueryContext(ctx, `SELECT `+desiredCols+` FROM desired_states WHERE server_id=? AND revision>=? ORDER BY revision DESC LIMIT ?`, v.ServerID, applied, maxRevisions+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	type historyKey struct {
		nodeID, shareID int64
		port            int
		protocol, name  string
	}
	current := map[historyKey]bool{}
	key := func(n NetworkHistoricalNode) historyKey {
		shareID := int64(0)
		if n.ShareID != nil {
			shareID = *n.ShareID
		}
		return historyKey{nodeID: n.NodeID, shareID: shareID, port: n.ListenPort, protocol: n.Protocol, name: n.Name}
	}
	for _, n := range v.Nodes {
		if n.Core == domain.CoreSingBox && !n.Revoked {
			var shareID *int64
			if n.ShareID != 0 {
				id := n.ShareID
				shareID = &id
			}
			current[key(NetworkHistoricalNode{NodeID: n.NodeID, Name: n.Name, Protocol: n.Protocol, ListenPort: n.ListenPort, ShareID: shareID})] = true
		}
	}
	known := map[historyKey]NetworkHistoricalNode{}
	currentForwards := map[NetworkHistoricalForward]bool{}
	knownForwards := map[NetworkHistoricalForward]bool{}
	for _, f := range v.Forwards {
		if !f.Retired {
			currentForwards[NetworkHistoricalForward{f.ForwardID, f.Revision, f.ListenPort}] = true
		}
	}
	var previous, smallest int64
	count, bytes := 0, 0
	foundApplied := applied == 0
	for rows.Next() {
		count++
		if count > maxRevisions {
			v.HistoryComplete = false
			break
		}
		ds, err := s.scanDesired(rows)
		if err != nil {
			return err
		}
		if previous != 0 && ds.Revision != previous-1 {
			v.HistoryComplete = false
		}
		previous, smallest = ds.Revision, ds.Revision
		if ds.Revision == applied {
			foundApplied = ds.Hash == appliedHash
		}
		bytes += len(ds.Payload)
		if len(ds.Payload) > 8<<20 || bytes > maxBytes {
			v.HistoryComplete = false
			break
		}
		// Decode only public metadata: never load protocol keys into the view.
		var payload struct {
			ServerID int64 `json:"server_id"`
			Nodes    []struct {
				NetworkHistoricalNode
				Core string `json:"core"`
			} `json:"nodes"`
			Forwards []struct {
				ForwardID int64 `json:"forward_id"`
				Revision  int64 `json:"revision"`
				Retired   bool  `json:"retired"`
				Config    struct {
					ListenPort int `json:"listen_port"`
				} `json:"config"`
			} `json:"forwards"`
		}
		if err = json.Unmarshal(ds.Payload, &payload); err != nil || payload.ServerID != v.ServerID || len(payload.Nodes) > MaxNetworkImpactNodes || len(payload.Forwards) > MaxPortForwards {
			v.HistoryComplete = false
			continue
		}
		for _, f := range payload.Forwards {
			if f.Retired {
				continue
			}
			if f.ForwardID < 1 || f.ForwardID > 0xffffff || f.Revision < 1 || f.Revision > MaxForwardRevisions || f.Config.ListenPort < 1 || f.Config.ListenPort > 65535 {
				v.HistoryComplete = false
				continue
			}
			key := NetworkHistoricalForward{f.ForwardID, f.Revision, f.Config.ListenPort}
			if currentForwards[key] || knownForwards[key] {
				continue
			}
			if len(knownForwards) >= MaxNetworkImpactNodes {
				v.HistoryComplete = false
				break
			}
			knownForwards[key] = true
		}
		for _, n := range payload.Nodes {
			if n.Core != "singbox" {
				continue
			}
			if n.NodeID <= 0 || n.ListenPort < 1 || n.ListenPort > 65535 || len(n.Name) > 512 || len(n.Protocol) > 64 {
				v.HistoryComplete = false
				continue
			}
			k := key(n.NetworkHistoricalNode)
			if current[k] {
				continue
			}
			if _, exists := known[k]; exists {
				continue
			}
			if len(known) >= MaxNetworkImpactNodes {
				v.HistoryComplete = false
				break
			}
			known[k] = n.NetworkHistoricalNode
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !foundApplied || (applied == 0 && smallest > 1) {
		v.HistoryComplete = false
	}
	for _, n := range known {
		v.HistoricalNodes = append(v.HistoricalNodes, n)
	}
	for f := range knownForwards {
		v.HistoricalForwards = append(v.HistoricalForwards, f)
	}
	sort.Slice(v.HistoricalForwards, func(i, j int) bool {
		a, b := v.HistoricalForwards[i], v.HistoricalForwards[j]
		if a.ForwardID != b.ForwardID {
			return a.ForwardID < b.ForwardID
		}
		if a.Revision != b.Revision {
			return a.Revision < b.Revision
		}
		return a.ListenPort < b.ListenPort
	})
	sort.Slice(v.HistoricalNodes, func(i, j int) bool {
		a, b := v.HistoricalNodes[i], v.HistoricalNodes[j]
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		if a.ListenPort != b.ListenPort {
			return a.ListenPort < b.ListenPort
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		var as, bs int64
		if a.ShareID != nil {
			as = *a.ShareID
		}
		if b.ShareID != nil {
			bs = *b.ShareID
		}
		return as < bs
	})
	return nil
}
