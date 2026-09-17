package agent

import (
	"context"
	"ctlvps/internal/agentbudget"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ctlvps/internal/agentproto"
)

// MeterRule holds only the non-secret accounting configuration needed to
// resume cleanup before accepting another desired revision.
type MeterRule struct {
	NodeID  int64  `json:"node_id"`
	Port    int    `json:"port"`
	Core    string `json:"core"`
	Blocked bool   `json:"blocked,omitempty"`
	Retired bool   `json:"retired,omitempty"`
}

func (r MeterRule) spec() agentproto.NodeSpec {
	return agentproto.NodeSpec{NodeID: r.NodeID, ListenPort: r.Port, Core: r.Core, Blocked: r.Blocked, Retired: r.Retired}
}

type Retirement struct {
	Batch     agentproto.MeterSettlement `json:"batch"`
	Nodes     []MeterIdentity            `json:"nodes"`
	Remaining []MeterRule                `json:"remaining"`
	Acked     bool                       `json:"acked,omitempty"`
}

// RetirementHost is the privileged boundary. A snapshot may be captured only
// after writers are quiescent; cleanup is idempotent and requires durable ACK.
type RetirementHost interface {
	FinalRead(context.Context, []MeterIdentity) ([]agentproto.PortCounter, error)
	Clean(context.Context, *Retirement) error
}
type retirementHost struct{ a *Agent }

func (h retirementHost) FinalRead(ctx context.Context, nodes []MeterIdentity) ([]agentproto.PortCounter, error) {
	a := h.a
	nftCounters, err := a.NFT.ReadNodes(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]agentproto.PortCounter{}
	for _, c := range nftCounters {
		byID[c.NodeID] = c
	}
	out := make([]agentproto.PortCounter, 0, len(nodes))
	for _, n := range nodes {
		if n.Core == "singbox" {
			c, ok := byID[n.NodeID]
			if !ok {
				return nil, fmt.Errorf("final counter unavailable for node %d", n.NodeID)
			}
			c.Epoch = a.meterEpoch(n)
			c.FromZero = true
			out = append(out, c)
		} else if n.Core == "snell" {
			r, err := a.Systemd.FinalSnellReading(ctx, n.NodeID)
			if err != nil {
				return nil, err
			}
			out = append(out, agentproto.PortCounter{NodeID: n.NodeID, Port: n.Port, Source: "systemd-v1", Epoch: r.Epoch, Rx: r.Rx, Tx: r.Tx, FromZero: true})
		} else {
			return nil, errors.New("unsupported retiring core")
		}
	}
	return out, nil
}
func (h retirementHost) Clean(ctx context.Context, p *Retirement) error {
	var remaining []agentproto.NodeSpec
	for _, r := range p.Remaining {
		remaining = append(remaining, r.spec())
	}
	var ids []int64
	for _, n := range p.Nodes {
		if n.Core == "singbox" {
			ids = append(ids, n.NodeID)
		}
	}
	if err := h.a.NFT.PruneNodes(ctx, remaining, ids); err != nil {
		return err
	}
	for _, n := range p.Nodes {
		if n.Core == "snell" {
			if err := h.a.Systemd.RemoveSnellMeter(ctx, n.NodeID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (a *Agent) meterEpoch(n MeterIdentity) string {
	epoch := a.epoch() + ":nft-node-v1"
	if n.Generation != "" {
		epoch += ":" + n.Generation
	}
	return epoch
}

// beginRetirement runs only after a successful apply. NodeRules has already
// frozen every retired mark; the Snell driver has stopped removed instances.
// Exactly one bounded batch is persisted, so downtime cannot grow a queue.
func (a *Agent) beginRetirement(ctx context.Context) error {
	if a.State.Retirement != nil || !a.finalMeters || !a.State.MeteringV1 || a.State.ApplyError != "" {
		return nil
	}
	a.mu.Lock()
	ds := a.desired
	a.mu.Unlock()
	if ds == nil {
		return nil
	}
	active := map[int64]agentproto.NodeSpec{}
	for _, n := range ds.Nodes {
		active[n.NodeID] = n
	}
	p := &Retirement{}
	for _, n := range a.State.MeterNodes {
		if _, ok := active[n.NodeID]; !ok && len(p.Nodes) < agentproto.SettlementBatchSize {
			p.Nodes = append(p.Nodes, n)
		}
	}
	if len(p.Nodes) == 0 {
		return nil
	}
	selected := map[int64]bool{}
	for _, n := range p.Nodes {
		selected[n.NodeID] = true
	}
	for _, n := range a.State.MeterNodes {
		if selected[n.NodeID] {
			continue
		}
		r := MeterRule{NodeID: n.NodeID, Port: n.Port, Core: n.Core, Retired: true, Blocked: true}
		if live, ok := active[n.NodeID]; ok {
			r.Port = live.ListenPort
			r.Core = live.Core
			r.Blocked = live.Blocked
			r.Retired = false
		}
		p.Remaining = append(p.Remaining, r)
	}
	counters, err := a.retirementHost.FinalRead(ctx, p.Nodes)
	if err != nil {
		return err
	}
	p.Batch = agentproto.MeterSettlement{TS: time.Now().UTC(), Counters: counters}
	p.Batch.ID = p.Batch.Digest()
	if err = p.Batch.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(raw) > agentbudget.SettlementBytes {
		return errors.New("final settlement exceeds batch budget")
	}
	a.State.Retirement = p
	if err = a.State.Save(a.StateDir); err != nil {
		a.State.Retirement = nil
		return err
	}
	return nil
}

// flushRetirement always precedes live sampling or applying another revision.
// ACK loss replays identical bytes. Cleanup failure replays only cleanup.
func (a *Agent) flushRetirement(ctx context.Context) error {
	p := a.State.Retirement
	if p == nil {
		return nil
	}
	if err := p.validate(); err != nil {
		return err
	}
	if !p.Acked {
		resp, err := a.Client.Heartbeat(ctx, agentproto.Heartbeat{Version: a.Version, FinalMeters: &p.Batch})
		if err != nil {
			return err
		}
		if resp.FinalMeterVersion < 1 || resp.FinalMeterAck != p.Batch.ID {
			return errors.New("controller did not acknowledge final meter snapshot")
		}
		p.Acked = true
		if err = a.State.Save(a.StateDir); err != nil {
			p.Acked = false
			return err
		}
	}
	if err := a.retirementHost.Clean(ctx, p); err != nil {
		return err
	}
	removed := map[int64]bool{}
	for _, n := range p.Nodes {
		removed[n.NodeID] = true
	}
	old := a.State.MeterNodes
	kept := make([]MeterIdentity, 0, len(old)-len(p.Nodes))
	for _, n := range old {
		if !removed[n.NodeID] {
			kept = append(kept, n)
		}
	}
	a.State.MeterNodes = kept
	a.State.Retirement = nil
	if err := a.State.Save(a.StateDir); err != nil {
		a.State.MeterNodes = old
		a.State.Retirement = p
		return err
	}
	return nil
}

// RetirementPending is bounded backpressure: finish the current history before
// accepting a newer configuration. Existing proxy processes continue running.
func (a *Agent) retirementPending(ctx context.Context) (bool, error) {
	if err := a.flushRetirement(ctx); err != nil {
		return true, err
	}
	if err := a.beginRetirement(ctx); err != nil {
		return true, err
	}
	return a.State.Retirement != nil, nil
}

func (p *Retirement) validate() error {
	if err := p.Batch.Validate(); err != nil {
		return err
	}
	if len(p.Nodes) != len(p.Batch.Counters) || len(p.Remaining) > maxMeterIdentities {
		return errors.New("incomplete retirement plan")
	}
	ids := map[int64]bool{}
	for _, n := range p.Nodes {
		if ids[n.NodeID] {
			return errors.New("duplicate retirement identity")
		}
		ids[n.NodeID] = true
	}
	for _, c := range p.Batch.Counters {
		if !ids[c.NodeID] {
			return errors.New("retirement snapshot identity mismatch")
		}
	}
	for _, n := range p.Remaining {
		if ids[n.NodeID] {
			return errors.New("retirement plan contains live identity")
		}
	}
	return nil
}
