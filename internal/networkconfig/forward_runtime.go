package networkconfig

import (
	"errors"
	"net/netip"
	"strings"
)

// LiteralTCPTarget is the initial compiler boundary. The stored model
// can represent later transports, but these must not silently become working
// listeners before their own endpoint authorization and lifecycle are wired.
func (f Forward) LiteralTCPTarget() (netip.Addr, error) {
	if f.Network != "tcp" {
		return netip.Addr{}, errors.New("此调用仅支持 TCP 固定转发")
	}
	return f.LiteralTarget()
}

func (f Forward) LiteralTarget() (netip.Addr, error) {
	if err := f.Validate(); err != nil {
		return netip.Addr{}, err
	}
	ip, err := HostAddress(f.TargetHost)
	if err != nil {
		return netip.Addr{}, errors.New("固定转发目标域名需要本机解析候选")
	}
	if NeedsTransportGrant(ip) {
		return netip.Addr{}, errors.New("固定转发私网目标需要独立本机授权及端点候选")
	}
	for _, prefix := range transportRestricted {
		if prefix.Contains(ip) {
			return netip.Addr{}, errors.New("固定转发目标不符合本机端点策略")
		}
	}
	return ip, nil
}

// ResolvedTCPTarget binds the fixed target to an agent-local address. Domain
// pins expire; private targets require independent forward authority.
func (f Forward) ResolvedTCPTarget(pin *ResolvedForwardTarget) (netip.Addr, error) {
	if f.Network != "tcp" {
		return netip.Addr{}, errors.New("此调用仅支持 TCP 固定转发")
	}
	return f.ResolvedTarget(pin)
}

func (f Forward) ResolvedTarget(pin *ResolvedForwardTarget) (netip.Addr, error) {
	if pin == nil {
		return f.LiteralTarget()
	}
	if err := f.Validate(); err != nil {
		return netip.Addr{}, err
	}
	if pin.Purpose != "" || pin.UDP || len(pin.TransportGrants) != 0 || pin.Port != f.TargetPort {
		return netip.Addr{}, errors.New("转发解析与固定目标不一致")
	}
	if ip, err := HostAddress(f.TargetHost); err == nil {
		if pin.Host != "" || pin.Address != ip.String() {
			return netip.Addr{}, errors.New("固定转发字面量端点不一致")
		}
	} else if pin.Host != strings.ToLower(strings.TrimSuffix(f.TargetHost, ".")) {
		return netip.Addr{}, errors.New("固定转发域名端点不一致")
	}
	for _, g := range pin.Grants {
		if g.EgressProfileID != f.EgressProfileID {
			return netip.Addr{}, errors.New("固定转发授权出口不一致")
		}
	}
	if err := pin.Validate(); err != nil {
		return netip.Addr{}, err
	}
	return HostAddress(pin.Address)
}
