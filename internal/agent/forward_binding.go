package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

func nodeResource(id int64) agentproto.ResourceIdentity {
	return agentproto.ResourceIdentity{Kind: "node", ID: id}
}
func forwardResource(id int64) agentproto.ResourceIdentity {
	return agentproto.ResourceIdentity{Kind: "forward", ID: id}
}

func (r *bindingRuntime) forwardStatus() map[int64]string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]string, len(r.forwardHealth))
	for id, message := range r.forwardHealth {
		if len(message) > 512 {
			message = "转发网络不可用，请查看 agent 日志"
		}
		out[id] = message
	}
	return out
}

func (r *bindingRuntime) refreshHealth(ctx context.Context, token string, snapshot *agentproto.NetworkSnapshot) (map[int64]string, map[int64]string, error) {
	if r.refreshResources == nil {
		// Compatibility is node-only. An old monitor can never grant a forward.
		nodes, err := r.refresh(ctx, token, snapshot)
		return nodes, nil, err
	}
	bad, err := r.refreshResources(ctx, token, snapshot)
	nodes, forwards := map[int64]string{}, map[int64]string{}
	for resource, message := range bad {
		if resource.Kind == "node" {
			nodes[resource.ID] = message
		} else if resource.Kind == "forward" {
			forwards[resource.ID] = message
		}
	}
	return nodes, forwards, err
}

func forwardFingerprint(f agentproto.ForwardSpec, ipv4Only bool) string {
	raw, _ := json.Marshal(struct {
		Forward  agentproto.ForwardSpec
		IPv4Only bool
		Grants   []networkconfig.ForwardGrant
	}{f, ipv4Only, f.ForwardGrants})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func managedBindingIDs(nodes []agentproto.NodeSpec, forwards []agentproto.ForwardSpec, plan *networkguard.Plan) map[string]bool {
	// Collect the same stable interface identities, with no synthetic node.
	ids := bindingIDs(nodes, plan)
	for _, f := range forwards {
		if f.Config.ListenInterfaceID != "" {
			ids[f.Config.ListenInterfaceID] = true
		}
		if d := f.OuterBinding(); d != nil {
			if d.InterfaceID != "" {
				ids[d.InterfaceID] = true
			}
			for _, source := range []*networkconfig.Address{d.SourceIPv4, d.SourceIPv6} {
				if source != nil {
					ids[source.InterfaceID] = true
				}
			}
		}
	}
	return ids
}

func (tx *bindingApply) stageForwards(ds *agentproto.DesiredState, old map[agentproto.ResourceIdentity]networkguard.Binding) {
	for _, f := range ds.Forwards {
		resource := forwardResource(f.ForwardID)
		tx.wanted[resource] = true
		config := f.Config
		family := ""
		if transport, ok := f.TransportConfig(); ok {
			family = transport.Family
		}
		b := networkguard.Binding{Pending: true, ForwardID: f.ForwardID, Forward: &config, ListenPort: config.ListenPort,
			Core: "singbox", Wanted: config.BindingPolicy(), IPv4Only: ds.IPv4Only, Fingerprint: forwardFingerprint(f, ds.IPv4Only), ForwardTransport: f.HasTransport(), ForwardFamily: family}
		if prior, ok := old[resource]; ok && !prior.Pending && !f.Blocked && prior.Fingerprint == b.Fingerprint {
			b = prior
		}
		tx.staged[resource] = b
	}
}

func (tx *bindingApply) prepareForwards(ctx context.Context, ds *agentproto.DesiredState) {
	if len(ds.Forwards) == 0 {
		return
	}
	snapshot := tx.runtime.collect()
	start := 0
	for i, f := range ds.Forwards {
		if f.ForwardID == tx.runtime.nextDNSForward {
			start = i
			break
		}
	}
	for step := range ds.Forwards {
		i := (start + step) % len(ds.Forwards)
		f := ds.Forwards[i]
		if snapshot == nil || time.Since(snapshot.SampledAt) > time.Second {
			snapshot = tx.runtime.collect()
		}
		if f.Blocked || f.Retired {
			continue
		}
		resource := forwardResource(f.ForwardID)
		if !agentproto.NetworkBindingSupported("singbox", ds.Versions["sing-box"].Version) {
			tx.failures = append(tx.failures, errors.New("固定转发所选内核版本尚未通过网络绑定验证"))
			continue
		}
		prior := tx.staged[resource].Applied.ForwardTarget
		if _, literal := networkconfig.HostAddress(f.Config.TargetHost); literal != nil && (prior == nil || prior.DNSRefreshDue(time.Now())) && ctx.Err() == nil {
			tx.runtime.nextDNSForward = ds.Forwards[(i+1)%len(ds.Forwards)].ForwardID
		}
		var resolved networkconfig.Resolved
		var err error
		if f.HasTransport() {
			resolved, err = netinventory.ResolveForwardTransport(f, snapshot, ds.IPv4Only, time.Now())
		} else {
			resolved, err = netinventory.ResolveForwardWithDNS(ctx, f, snapshot, ds.IPv4Only, time.Now(), prior)
		}
		if err == nil && resolved.SOCKS5 != nil {
			snapshot = tx.runtime.collect()
			endpoint, pin := resolved.SOCKS5, resolved.ForwardTarget
			resolved, err = netinventory.ResolveBinding(f.Config.BindingPolicy(), f.OuterBinding(), snapshot, ds.IPv4Only, time.Now())
			if err == nil {
				err = netinventory.ValidateSOCKS5Endpoint(*endpoint, snapshot)
			}
			if err == nil {
				err = netinventory.ValidateResolvedForwardTarget(f.Config, pin, snapshot)
			}
			resolved.SOCKS5, resolved.ForwardTarget = endpoint, pin
		} else if err == nil && resolved.ForwardTarget != nil {
			snapshot = tx.runtime.collect()
			pin := resolved.ForwardTarget
			resolved, err = netinventory.ResolveBinding(f.Config.BindingPolicy(), f.Direct, snapshot, ds.IPv4Only, time.Now())
			if err == nil {
				err = netinventory.ValidateResolvedForwardTarget(f.Config, pin, snapshot)
				resolved.ForwardTarget = pin
			}
		}
		if err != nil {
			tx.staged[resource] = pendingBinding(tx.staged[resource])
			tx.failures = append(tx.failures, fmt.Errorf("forward %d network: %w", f.ForwardID, err))
			continue
		}
		b := tx.staged[resource]
		comparable := resolved
		comparable.Sequence = b.Applied.Sequence
		if !b.Pending && sameResolvedPath(comparable, b.Applied) {
			resolved.Sequence = b.Applied.Sequence
		} else if !b.Pending {
			tx.staged[resource] = pendingBinding(b)
		}
		b.Pending, b.Applied = false, resolved
		candidate := networkguard.Plan{Token: tx.runtime.plan.Token, Bindings: []networkguard.Binding{b}}
		if err := candidate.Validate(); err != nil {
			tx.staged[resource] = pendingBinding(b)
			tx.failures = append(tx.failures, fmt.Errorf("forward %d network: %w", f.ForwardID, err))
			continue
		}
		f.RuntimeNetwork = &resolved
		tx.forwards = append(tx.forwards, f)
		tx.candidates[resource] = b
	}
}
