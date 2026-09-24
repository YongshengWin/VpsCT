package netinventory

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

func ValidateForwardTarget(f networkconfig.Forward, snapshot *agentproto.NetworkSnapshot) error {
	ip, err := f.LiteralTarget()
	if err != nil {
		return err
	}
	return validateRemoteTransportAddress(ip.String(), snapshot)
}

func ResolveForward(f agentproto.ForwardSpec, snapshot *agentproto.NetworkSnapshot, ipv4Only bool, now time.Time) (networkconfig.Resolved, error) {
	if err := f.Validate(); err != nil {
		return networkconfig.Resolved{}, err
	}
	pin, err := ResolveForwardLiteral(f, snapshot)
	if err != nil {
		return networkconfig.Resolved{}, err
	}
	r, err := ResolveBinding(f.Config.BindingPolicy(), f.Direct, snapshot, ipv4Only, now)
	r.ForwardTarget = pin
	return r, err
}

// ResolveForwardTransport pins a public literal upstream and a separate fixed
// target. The two identities never share a node grant or a direct fallback.
func ResolveForwardTransport(f agentproto.ForwardSpec, snapshot *agentproto.NetworkSnapshot, ipv4Only bool, now time.Time) (networkconfig.Resolved, error) {
	if !f.HasTransport() {
		return networkconfig.Resolved{}, errors.New("固定转发缺少中转出口")
	}
	if err := f.ValidateTargetSelection(); err != nil {
		return networkconfig.Resolved{}, err
	}
	cfg, _ := f.TransportConfig()
	if _, err := networkconfig.HostAddress(cfg.Server); err != nil {
		return networkconfig.Resolved{}, errors.New("固定转发中转端点须使用公网字面量 IP")
	}
	r, err := ResolveSOCKS5WithGrants(context.Background(), f.ForwardID, f.Config.BindingPolicy(), cfg, snapshot, ipv4Only, now, nil)
	if err != nil {
		return networkconfig.Resolved{}, err
	}
	if _, err = r.SOCKS5.WithTransportGrants(f.ForwardID, f.Config.EgressProfileID, nil); err != nil {
		return networkconfig.Resolved{}, err
	}
	pin, err := ResolveForwardLiteral(f, snapshot)
	if err != nil {
		return networkconfig.Resolved{}, err
	}
	r.ForwardTarget = pin
	return r, nil
}

func ValidateForwardSelection(f networkconfig.Forward, direct *networkconfig.Direct, snapshot *agentproto.NetworkSnapshot) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if _, err := networkconfig.HostAddress(f.TargetHost); err == nil {
		return ValidateForwardTarget(f, snapshot)
	}
	if direct == nil {
		return errors.New("域名目标需要选择带 DNS 的直连出口")
	}
	return ValidateBootstrapEndpoint(0, 0, direct.DNS, snapshot, nil)
}
func ValidateResolvedForwardTarget(f networkconfig.Forward, pin *networkconfig.ResolvedForwardTarget, snapshot *agentproto.NetworkSnapshot) error {
	ip, err := f.ResolvedTarget(pin)
	if err != nil {
		return err
	}
	return validateRemoteTransportAddress(ip.String(), snapshot)
}
func ResolveForwardWithDNS(ctx context.Context, f agentproto.ForwardSpec, snapshot *agentproto.NetworkSnapshot, ipv4Only bool, now time.Time, pin *networkconfig.ResolvedForwardTarget) (networkconfig.Resolved, error) {
	if _, err := networkconfig.HostAddress(f.Config.TargetHost); err == nil {
		return ResolveForward(f, snapshot, ipv4Only, now)
	}
	var empty networkconfig.Resolved
	if err := f.Validate(); err != nil {
		return empty, err
	}
	if err := ValidateForwardSelectionWithGrants(f, snapshot); err != nil {
		return empty, err
	}
	r, err := ResolveBinding(f.Config.BindingPolicy(), f.Direct, snapshot, ipv4Only, now)
	if err != nil {
		return empty, err
	}
	if pin != nil && !pin.DNSRefreshDue(now) && pin.ValidateForForward(f.ForwardID, f.Config.EgressProfileID) == nil && pin.StillAuthorized(f.ForwardGrants) {
		if err := ValidateResolvedForwardTarget(f.Config, pin, snapshot); err != nil {
			return empty, err
		}
		r.ForwardTarget = pin
		return r, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	families := []string{r.Direct.Config.Family}
	if families[0] == "dual" {
		families = []string{"ipv4", "ipv6"}
	}
	host := strings.ToLower(strings.TrimSuffix(f.Config.TargetHost, "."))
	for _, family := range families {
		started := time.Now()
		result, err := LookupForwardBootstrap(ctx, f.ForwardID, host, family, *r.Direct)
		if err != nil {
			return empty, err
		}
		sort.Strings(result.Addresses)
		for _, address := range result.Addresses {
			candidate := networkconfig.ResolvedForwardTarget{ResolvedSOCKS5: networkconfig.ResolvedSOCKS5{Host: host, Address: address, Port: f.Config.TargetPort}}
			candidate.SetDNSLifetime(started, result.TTLSeconds)
			candidate, err = candidate.WithGrants(f.ForwardID, f.Config.EgressProfileID, f.ForwardGrants)
			if err != nil {
				return empty, err
			}
			if err := ValidateResolvedForwardTarget(f.Config, &candidate, snapshot); err != nil {
				return empty, err
			}
		}
		if len(result.Addresses) > 0 {
			r.ForwardTarget = &networkconfig.ResolvedForwardTarget{ResolvedSOCKS5: networkconfig.ResolvedSOCKS5{Host: host, Address: result.Addresses[0], Port: f.Config.TargetPort}}
			r.ForwardTarget.SetDNSLifetime(started, result.TTLSeconds)
			approved, err := r.ForwardTarget.WithGrants(f.ForwardID, f.Config.EgressProfileID, f.ForwardGrants)
			r.ForwardTarget = &approved
			return r, err
		}
	}
	return empty, errors.New("固定目标 DNS 未返回可用地址")
}

// ResolveForwardLiteral preserves legacy public candidates and attaches only
// independently authorized private target pins.
func ResolveForwardLiteral(f agentproto.ForwardSpec, snapshot *agentproto.NetworkSnapshot) (*networkconfig.ResolvedForwardTarget, error) {
	ip, err := networkconfig.HostAddress(f.Config.TargetHost)
	if err != nil {
		return nil, err
	}
	if !networkconfig.NeedsTransportGrant(ip) {
		return nil, ValidateForwardTarget(f.Config, snapshot)
	}
	pin, err := (networkconfig.ResolvedForwardTarget{ResolvedSOCKS5: networkconfig.ResolvedSOCKS5{Address: ip.String(), Port: f.Config.TargetPort}}).WithGrants(f.ForwardID, f.Config.EgressProfileID, f.ForwardGrants)
	if err != nil {
		return nil, err
	}
	return &pin, ValidateResolvedForwardTarget(f.Config, &pin, snapshot)
}

func ValidateForwardSelectionWithGrants(f agentproto.ForwardSpec, snapshot *agentproto.NetworkSnapshot) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if ip, err := networkconfig.HostAddress(f.Config.TargetHost); err == nil && networkconfig.NeedsTransportGrant(ip) {
		_, err = ResolveForwardLiteral(f, snapshot)
		return err
	}
	return ValidateForwardSelection(f.Config, f.Direct, snapshot)
}
