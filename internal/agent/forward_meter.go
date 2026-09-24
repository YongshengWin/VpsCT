package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentproto"
)

type forwardPending struct {
	Receipt agentproto.ForwardReceipt `json:"receipt"`
	Retired []int64                   `json:"retired"`
	Acked   bool                      `json:"acked"`
}

func (a *Agent) prepareForwardMeters(ctx context.Context, ds *agentproto.DesiredState) error {
	if ds.NetworkForwardVersion == 0 && len(a.State.ForwardMeters) == 0 {
		return nil
	}
	known := map[int64]bool{}
	wanted := make([]agentproto.ForwardMeterRule, 0, len(ds.Forwards))
	for _, f := range ds.Forwards {
		if f.Retired && a.State.ForwardAckRevision >= ds.Revision {
			continue
		}
		known[f.ForwardID] = true
		wanted = append(wanted, agentproto.ForwardMeterRule{ForwardID: f.ForwardID, ListenPort: f.Config.ListenPort, Blocked: f.Blocked, Retired: f.Retired})
	}
	for _, meter := range a.State.ForwardMeters {
		if !known[meter.ForwardID] {
			meter.Blocked, meter.Retired = true, true
			wanted = append(wanted, meter)
		}
	}
	if len(wanted) > 2*agentbudget.ActiveForwards {
		return errors.New("固定转发计量清理队列已满")
	}
	a.State.ForwardMeters = wanted
	if !a.NFT.ForwardsExist(ctx) || a.State.ForwardNonce == "" {
		a.State.ForwardNonce = nonce()
	}
	if err := a.State.Save(a.StateDir); err != nil {
		return err
	}
	return a.NFT.EnsureForwards(ctx, wanted)
}

func (a *Agent) forwardCounters(ctx context.Context) ([]agentproto.ForwardCounter, error) {
	if len(a.State.ForwardMeters) == 0 {
		return nil, nil
	}
	values, err := a.NFT.ReadForwards(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]agentproto.ForwardCounter{}
	for _, c := range values {
		c.Epoch = a.bootID + ":forward:" + a.State.ForwardNonce
		byID[c.ForwardID] = c
	}
	out := make([]agentproto.ForwardCounter, 0, len(a.State.ForwardMeters))
	for _, m := range a.State.ForwardMeters {
		c, ok := byID[m.ForwardID]
		if !ok {
			return nil, fmt.Errorf("转发 %d 的计量快照缺失", m.ForwardID)
		}
		out = append(out, c)
	}
	return out, agentproto.ValidateForwardCounters(out)
}

func (a *Agent) freezeForwardReceipt(ctx context.Context, ds *agentproto.DesiredState) error {
	if len(ds.Forwards) == 0 || a.State.ForwardAckRevision >= ds.Revision {
		return nil
	}
	if a.State.ForwardPending != nil {
		return errors.New("上次转发应用回执尚未确认")
	}
	values, err := a.forwardCounters(ctx)
	if err != nil {
		return err
	}
	byID := map[int64]agentproto.ForwardCounter{}
	for _, c := range values {
		byID[c.ForwardID] = c
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	p := &forwardPending{Receipt: agentproto.ForwardReceipt{ID: hex.EncodeToString(id[:]), Revision: ds.Revision, Hash: ds.Hash, TS: time.Now().UTC()}}
	for _, f := range ds.Forwards {
		c, ok := byID[f.ForwardID]
		if !ok {
			return errors.New("转发应用缺少最终计量身份")
		}
		p.Receipt.Counters = append(p.Receipt.Counters, c)
		if f.Retired {
			p.Retired = append(p.Retired, f.ForwardID)
		}
	}
	if err := p.Receipt.Validate(); err != nil {
		return err
	}
	a.State.ForwardPending = p
	return a.State.Save(a.StateDir)
}

func (a *Agent) flushForwardReceipt(ctx context.Context) error {
	p := a.State.ForwardPending
	if p == nil {
		return nil
	}
	if !p.Acked {
		response, err := a.Client.Heartbeat(ctx, agentproto.Heartbeat{ForwardReceipt: &p.Receipt})
		if err != nil {
			return err
		}
		if response.ForwardReceiptAck != p.Receipt.ID {
			return errors.New("控制端尚未确认转发应用与计量回执")
		}
		p.Acked = true
		if err := a.State.Save(a.StateDir); err != nil {
			p.Acked = false
			return err
		}
	}
	retired := map[int64]bool{}
	for _, id := range p.Retired {
		retired[id] = true
	}
	var kept []agentproto.ForwardMeterRule
	for _, m := range a.State.ForwardMeters {
		if !retired[m.ForwardID] {
			kept = append(kept, m)
		}
	}
	if len(p.Retired) > 0 {
		release, err := lockConfiguration()
		if err != nil {
			return err
		}
		defer release()
		if !a.NFT.ForwardsExist(ctx) {
			a.State.ForwardNonce = nonce()
			if err := a.State.Save(a.StateDir); err != nil {
				return err
			}
		}
		if err := a.NFT.PruneForwards(ctx, kept, p.Retired); err != nil {
			return err
		}
	}
	previousRevision, previousMeters := a.State.ForwardAckRevision, a.State.ForwardMeters
	a.State.ForwardAckRevision = p.Receipt.Revision
	a.State.ForwardMeters, a.State.ForwardPending = kept, nil
	if err := a.State.Save(a.StateDir); err != nil {
		a.State.ForwardAckRevision, a.State.ForwardMeters, a.State.ForwardPending = previousRevision, previousMeters, p
		return err
	}
	return nil
}
