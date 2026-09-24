package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"ctlvps/internal/agentproto"
)

func (a *Agent) billingPolicy() agentproto.NetworkBillingPolicy {
	if a.State.NetworkBillingPolicy == nil {
		return agentproto.LegacyNetworkBilling()
	}
	return *a.State.NetworkBillingPolicy
}

func (a *Agent) billingPreferences() {
	if a.network == nil {
		return
	}
	ids := map[string]bool{}
	for _, p := range []*agentproto.NetworkBillingPolicy{a.State.NetworkBillingPolicy, a.State.NetworkBillingRequested} {
		if p != nil {
			for _, id := range p.InterfaceIDs {
				ids[id] = true
			}
		}
	}
	a.network.SetPreferredIDs(ids)
}

func (a *Agent) flushNetworkBilling(ctx context.Context) error {
	sw := a.State.NetworkBillingPending
	if sw == nil {
		return nil
	}
	if err := sw.Validate(); err != nil {
		return err
	}
	if !a.billingPendingSaved {
		if err := a.State.Save(a.StateDir); err != nil {
			return err
		}
		a.billingPendingSaved = true
	}
	// Legacy wire counters stay empty so an old controller cannot mistake this
	// frozen settlement for a normal heartbeat after a controller rollback.
	resp, err := a.Client.Heartbeat(ctx, agentproto.Heartbeat{NetworkBillingSwitch: sw})
	if err != nil {
		return err
	}
	if resp.NetworkBillingVersion < agentproto.NetworkBillingVersion || resp.NetworkBillingAck != sw.ID {
		return errors.New("等待控制端确认计费切换")
	}
	oldPolicy := a.State.NetworkBillingPolicy
	a.State.NetworkBillingPolicy = &sw.Next
	a.State.NetworkBillingPending = nil
	if err = a.State.Save(a.StateDir); err != nil {
		a.State.NetworkBillingPolicy = oldPolicy
		a.State.NetworkBillingPending = sw
		return err
	}
	a.billingPendingSaved = false
	return nil
}

func (a *Agent) receiveNetworkBilling(ctx context.Context, resp agentproto.HeartbeatResponse) error {
	if resp.NetworkBillingVersion < agentproto.NetworkBillingVersion {
		return nil
	}
	if a.State.NetworkBillingPending != nil {
		return nil
	}
	current, next := resp.NetworkBillingCurrent, resp.NetworkBillingRequested
	if current == nil || next == nil || current.Validate() != nil || next.Validate() != nil || next.Revision < current.Revision {
		return errors.New("控制端计费策略无效")
	}
	if current.Revision == next.Revision && a.State.NetworkBillingPolicy != nil && a.State.NetworkBillingRequested != nil && a.State.NetworkBillingPolicy.Equal(*current) && a.State.NetworkBillingRequested.Equal(*next) {
		return nil
	}
	// The controller's committed revision is authoritative after agent state loss.
	previousPolicy, previousRequest := a.State.NetworkBillingPolicy, a.State.NetworkBillingRequested
	a.State.NetworkBillingPolicy, a.State.NetworkBillingRequested = current, next
	if err := a.State.Save(a.StateDir); err != nil {
		a.State.NetworkBillingPolicy, a.State.NetworkBillingRequested = previousPolicy, previousRequest
		return err
	}
	if current.Revision == next.Revision {
		return nil
	}
	if a.network == nil {
		return errors.New("网卡采集尚未准备好")
	}
	a.billingPreferences()
	metrics := a.Metrics.Collect()
	snapshot := a.network.Collect()
	legacy := agentproto.LegacyNetworkCounters{Epoch: a.epoch()}
	if current.Mode == "legacy" || next.Mode == "legacy" {
		var err error
		legacy, err = legacyNetworkBoundary(snapshot, metrics.Interface, a.epoch())
		if err != nil {
			return err
		}
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	sw := &agentproto.NetworkBillingSwitch{ID: hex.EncodeToString(id[:]), PreviousRevision: current.Revision, Next: *next, TS: snapshot.SampledAt, Legacy: legacy, Snapshot: snapshot}
	if err := sw.Validate(); err != nil {
		return err
	}
	a.State.NetworkBillingPending = sw
	a.billingPendingSaved = false
	if err := a.State.Save(a.StateDir); err != nil {
		return err
	}
	a.billingPendingSaved = true
	return a.flushNetworkBilling(ctx)
}

func (a *Agent) addNetworkBillingReport(hb *agentproto.Heartbeat) {
	p := a.billingPolicy()
	hb.NetworkBillingRevision = p.Revision
	hb.NetworkBillingHeld = a.State.NetworkBillingPending != nil
	if p.Revision == 0 && !hb.NetworkBillingHeld {
		return
	}
	hb.NetworkBillingLegacy = &agentproto.LegacyNetworkCounters{Epoch: hb.Epoch, Rx: hb.Metrics.NetRx, Tx: hb.Metrics.NetTx}
	hb.Metrics.NetRx, hb.Metrics.NetTx = 0, 0
	if p.Mode != "interfaces" {
		return
	}
	// The headline rate follows the selected billing set. Missing rates remain
	// zero with an explicit diagnostic; interface rows retain their own values.
	hb.Metrics.NetRxRate, hb.Metrics.NetTxRate = 0, 0
	hb.Metrics.Interface = ""
	if hb.Metrics.Network == nil {
		hb.Diagnostics.NetworkBillingError = "计费网卡采样暂不可用"
		return
	}
	byID := map[string]agentproto.NetworkInterface{}
	for _, n := range hb.Metrics.Network.Interfaces {
		byID[n.ID] = n
	}
	for _, id := range p.InterfaceIDs {
		n, ok := byID[id]
		if !ok || !n.RateValid {
			hb.Diagnostics.NetworkBillingError = "部分计费网卡没有有效速率"
			continue
		}
		hb.Metrics.NetRxRate += n.RxRate
		hb.Metrics.NetTxRate += n.TxRate
	}
}
