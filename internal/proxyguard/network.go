package proxyguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/secureupdate"
)

// Network digest excludes only bounded, expiring readiness elements. Removing a
// rule, changing a timeout, adding a permanent grant or changing a set's type is
// drift. The watchdog restores static policy with EMPTY leases, never health.
func networkDigest(raw []byte) (string, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	for _, entry := range doc.Nftables {
		b, ok := entry["set"]
		if !ok {
			continue
		}
		var set map[string]any
		if err := json.Unmarshal(b, &set); err != nil {
			return "", err
		}
		name, _ := set["name"].(string)
		if _, ok := networkguard.ParseLeaseSetName(name); !ok {
			continue
		}
		if set["type"] != "nf_proto" || set["timeout"] != float64(networkguard.Lease/time.Second) || set["size"] != float64(2) {
			return "", errors.New("invalid network lease set")
		}
		if elements, exists := set["elem"]; exists {
			list, ok := elements.([]any)
			if !ok || len(list) > 2 {
				return "", errors.New("invalid network lease elements")
			}
			seen := map[string]bool{}
			for _, rawElement := range list {
				wrapper, ok := rawElement.(map[string]any)
				if !ok || len(wrapper) != 1 {
					return "", errors.New("permanent network lease")
				}
				element, ok := wrapper["elem"].(map[string]any)
				if !ok {
					return "", errors.New("invalid network lease")
				}
				value, _ := element["val"].(string)
				remaining, timed := element["expires"].(float64)
				if (value != "ipv4" && value != "ipv6") || seen[value] || !timed || remaining < 0 || remaining > networkguard.Lease.Seconds() {
					return "", errors.New("unbounded network lease")
				}
				seen[value] = true
				for key, val := range element {
					switch key {
					case "val", "expires":
					case "timeout":
						seconds, ok := val.(float64)
						if !ok || seconds <= 0 || seconds > networkguard.Lease.Seconds() {
							return "", errors.New("invalid network lease duration")
						}
					default:
						return "", errors.New("unexpected network lease field")
					}
				}
			}
			delete(set, "elem")
		}
		encoded, err := json.Marshal(set)
		if err != nil {
			return "", err
		}
		entry["set"] = encoded
	}
	// nft 1.0 and 1.1 emit sets/chains in a different order. Their declaration
	// order does not change enforcement; rule order does and remains intact.
	type declaration struct {
		key   string
		entry map[string]json.RawMessage
	}
	var declarations []declaration
	var rules []map[string]json.RawMessage
	for _, entry := range doc.Nftables {
		if len(entry) != 1 {
			return "", errors.New("invalid network table entry")
		}
		if _, ok := entry["metainfo"]; ok {
			continue
		}
		if _, ok := entry["rule"]; ok {
			rules = append(rules, entry)
			continue
		}
		for kind, raw := range entry {
			var identity struct{ Family, Table, Name string }
			if err := json.Unmarshal(raw, &identity); err != nil {
				return "", err
			}
			declarations = append(declarations, declaration{kind + "/" + identity.Family + "/" + identity.Table + "/" + identity.Name, entry})
		}
	}
	sort.Slice(declarations, func(i, j int) bool { return declarations[i].key < declarations[j].key })
	doc.Nftables = nil
	for _, d := range declarations {
		doc.Nftables = append(doc.Nftables, d.entry)
	}
	doc.Nftables = append(doc.Nftables, rules...)
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return digest(b)
}

func liveNetworkDigest(ctx context.Context) (string, error) {
	b, err := command(ctx, "", "nft", "-j", "list", "table", "inet", networkguard.Table)
	if err != nil {
		return "", err
	}
	return networkDigest(b)
}

// LoadNetwork returns only the current-boot root-owned plan. In particular a
// process restart cannot infer an applied binding from controller input.
func LoadNetwork(ctx context.Context) (*networkguard.Plan, error) {
	var plan *networkguard.Plan
	err := locked(ctx, func() error {
		p, err := readPolicy()
		if err == nil {
			plan = p.Network
		}
		return err
	})
	return plan, err
}

// InstallNetwork records intent before changing rules, so watchdog recovery
// cannot restore an older, less restrictive binding after partial application.
// A fresh token identifies every application, including retries/rollbacks.
func InstallNetwork(ctx context.Context, plan networkguard.Plan) error {
	rules, err := plan.Rules()
	if err != nil {
		return err
	}
	return locked(ctx, func() error {
		if err := verifyTransportAuthority(plan); err != nil {
			return err
		}
		p, err := readPolicy()
		if err != nil {
			return err
		}
		if p.Network != nil && p.Network.Token == plan.Token {
			old, _ := json.Marshal(p.Network)
			next, _ := json.Marshal(plan)
			if !bytes.Equal(old, next) {
				return errors.New("network guard application token content changed")
			}
			return repairNetwork(ctx, &p)
		}
		admission, err := admissionTransition(ctx, &p, plan)
		if err != nil {
			return err
		}
		p.Network, p.NetworkDigest = &plan, ""
		if admission != "" {
			// If interrupted before the transaction/digest completes, recovery
			// must stop the old core before rebuilding stateful admission.
			p.AdmissionDigest, p.AdmissionReset = "", true
			rules += admission
		}
		if p.TransportGuard {
			p.Digest = ""
		}
		if err := writePolicy(p); err != nil {
			return err
		}
		if p.TransportGuard {
			general, err := p.egressRules()
			if err != nil {
				return err
			}
			rules = "add table inet ctlvps_egress\ndelete table inet ctlvps_egress\n" + general + rules
		} else if _, err := p.egressRules(); err != nil {
			return err
		}
		// Even a failed nft preflight must not let the watchdog revive the
		// previous, less restrictive application after credentials changed.
		if _, err := command(ctx, rules, "nft", "--check", "-f", "-"); err != nil {
			return err
		}
		if _, err := command(ctx, rules, "nft", "-f", "-"); err != nil {
			return err
		}
		p.NetworkDigest, err = liveNetworkDigest(ctx)
		if err != nil {
			return err
		}
		if admission != "" {
			p.AdmissionDigest, err = liveAdmissionDigest(ctx)
			if err != nil {
				return err
			}
			p.AdmissionReset = false
		}
		if p.TransportGuard {
			p.Digest, err = liveDigest(ctx)
			if err != nil {
				return err
			}
		}
		return writePolicy(p)
	})
}

// RenewNetworkDNS changes no packet rule or lease. The coordinator has already
// queried and validated the same endpoint; stale tokens cannot extend a new plan.
func RenewNetworkDNS(ctx context.Context, next networkguard.Plan) error {
	if err := next.Validate(); err != nil {
		return err
	}
	return locked(ctx, func() error {
		if err := verifyTransportAuthority(next); err != nil {
			return err
		}
		p, err := readPolicy()
		if err != nil {
			return err
		}
		if p.Network == nil || p.Network.Token != next.Token || !p.Network.SamePaths(next) {
			return errors.New("DNS renewal changed network application or endpoint")
		}
		for _, b := range next.Bindings {
			if pin := b.Applied.SOCKS5; pin != nil && pin.DNSExpired(time.Now()) {
				return errors.New("DNS renewal is already expired")
			}
		}
		p.Network = &next
		return writePolicy(p)
	})
}

func repairNetwork(ctx context.Context, p *policy) error {
	return repairPolicy(ctx, p)
}

// RefreshNetwork must receive the agent's own snapshot, never a remote payload.
// The plan token rejects a stale monitor after an apply/rollback. Holding the
// same lock as installation and watchdog repair makes renewal transactional.
func RefreshNetwork(ctx context.Context, token string, snapshot *agentproto.NetworkSnapshot) (map[int64]string, error) {
	failures, err := refreshResources(ctx, token, snapshot, false)
	nodes := map[int64]string{}
	for resource, message := range failures {
		if resource.Kind == "node" {
			nodes[resource.ID] = message
		}
	}
	return nodes, err
}

func RefreshResources(ctx context.Context, token string, snapshot *agentproto.NetworkSnapshot) (map[agentproto.ResourceIdentity]string, error) {
	return refreshResources(ctx, token, snapshot, true)
}

func refreshResources(ctx context.Context, token string, snapshot *agentproto.NetworkSnapshot, forwards bool) (map[agentproto.ResourceIdentity]string, error) {
	var failures map[agentproto.ResourceIdentity]string
	err := locked(ctx, func() error {
		p, err := readPolicy()
		if err != nil {
			return err
		}
		if p.Network == nil || p.Network.Token != token {
			return errors.New("network guard application changed")
		}
		if err := repairNetwork(ctx, &p); err != nil {
			return err
		}
		ready, bad := p.Network.HealthyResources(snapshot, time.Now())
		local, localErr := secureupdate.LoadPolicy()
		for _, b := range p.Network.Bindings {
			resource := b.Resource()
			if resource.Kind == "forward" && p.AdmissionStopped {
				delete(ready, resource)
				bad[resource] = "连接预算已恢复，等待共享进程重新应用"
			}
			if resource.Kind == "forward" && !forwards {
				delete(ready, resource)
				bad[resource] = "旧监测器不能为固定转发续期"
			}
			pin := b.Applied.SOCKS5
			if target := b.Applied.ForwardTarget; target != nil && len(target.Grants) > 0 && (localErr != nil || !target.StillAuthorized(local.ForwardGrants)) {
				delete(ready, resource)
				bad[resource] = "私网固定转发授权已失效，等待重新授权和应用"
			}
			if pin != nil && len(pin.TransportGrants) > 0 && (localErr != nil || !pin.StillAuthorized(local.TransportGrants)) {
				delete(ready, resource)
				bad[resource] = "私网中转的本机授权已失效，等待重新授权和应用"
			}
		}
		failures = bad
		rules, err := p.Network.RenewResources(ready)
		if err != nil {
			return err
		}
		if rules == "" {
			return nil
		}
		_, err = command(ctx, rules, "nft", "-f", "-")
		return err
	})
	return failures, err
}

func verifyTransportAuthority(plan networkguard.Plan) error {
	var loaded bool
	var local secureupdate.Policy
	var err error
	for _, b := range plan.Bindings {
		if target := b.Applied.ForwardTarget; target != nil && len(target.Grants) > 0 {
			if !loaded {
				local, err = secureupdate.LoadPolicy()
				loaded = true
			}
			if err != nil || !target.StillAuthorized(local.ForwardGrants) {
				return errors.New("私网固定转发本机授权已失效")
			}
		}
		pin := b.Applied.SOCKS5
		if pin == nil || len(pin.TransportGrants) == 0 {
			continue
		}
		if !loaded {
			local, err = secureupdate.LoadPolicy()
			loaded = true
		}
		if err != nil || !pin.StillAuthorized(local.TransportGrants) {
			return errors.New("私网中转的本机端点授权已失效")
		}
	}
	return nil
}

func RevokeNetwork(ctx context.Context, token string, indices map[int]bool, lost bool) error {
	return locked(ctx, func() error {
		p, err := readPolicy()
		if err != nil {
			return err
		}
		if p.Network == nil || p.Network.Token != token {
			return fmt.Errorf("network guard application changed")
		}
		rules, err := p.Network.RevokeResources(p.Network.AffectedResources(indices, lost))
		if err != nil || rules == "" {
			return err
		}
		_, err = command(ctx, rules, "nft", "-f", "-")
		return err
	})
}
