package networkconfig

import (
	"errors"
	"net/netip"
	"sort"
)

// ForwardGrant is root-local authority for one fixed TCP destination. It has
// its own namespace: node permissions and transport grants never authorize it.
type ForwardGrant struct {
	ForwardID       int64  `json:"forward_id"`
	EgressProfileID int64  `json:"egress_profile_id"`
	Address         string `json:"address"`
	Port            int    `json:"port"`
}

func (g ForwardGrant) Validate() error {
	if g.ForwardID < 1 || g.ForwardID > 0xffffff || g.EgressProfileID < 0 || g.EgressProfileID > 1<<53-1 || g.Port < 1 || g.Port > 65535 {
		return errors.New("固定转发授权身份或端口无效")
	}
	// Share only address syntax and the grantable private ranges, not authority.
	p, err := (TransportGrant{Address: g.Address}).prefix()
	if err != nil {
		return err
	}
	for _, private := range grantableTransport {
		if p.Addr().BitLen() == private.Addr().BitLen() && p.Bits() >= private.Bits() && private.Contains(p.Addr()) {
			return nil
		}
	}
	return errors.New("固定转发授权仅接受 RFC1918、CGNAT 或 IPv6 ULA")
}

func ValidateForwardGrants(grants []ForwardGrant) error {
	if len(grants) > MaxTransportGrants {
		return errors.New("固定转发本机授权数量超限")
	}
	seen, counts := map[ForwardGrant]bool{}, map[int64]int{}
	for _, g := range grants {
		if err := g.Validate(); err != nil {
			return err
		}
		counts[g.ForwardID]++
		if seen[g] || counts[g.ForwardID] > MaxBindingTransportGrants {
			return errors.New("固定转发本机授权重复或超限")
		}
		seen[g] = true
	}
	return nil
}

func (g ForwardGrant) Matches(id, profile int64, ip netip.Addr, port int) bool {
	p, err := (TransportGrant{Address: g.Address}).prefix()
	return err == nil && g.ForwardID == id && g.EgressProfileID == profile && g.Port == port && p.Contains(ip)
}

func ForwardGrantsFor(grants []ForwardGrant, id, profile int64) []ForwardGrant {
	var out []ForwardGrant
	for _, g := range grants {
		if g.ForwardID == id && g.EgressProfileID == profile {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Address != out[j].Address {
			return out[i].Address < out[j].Address
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// ResolvedForwardTarget keeps the existing public DNS pin shape, but its
// private authority is separate and cannot be consumed by node transports.
type ResolvedForwardTarget struct {
	ResolvedSOCKS5
	Grants []ForwardGrant `json:",omitempty"`
}

func (r ResolvedForwardTarget) WithoutDNSLifetime() ResolvedForwardTarget {
	r.ResolvedSOCKS5 = r.ResolvedSOCKS5.WithoutDNSLifetime()
	return r
}

func (r ResolvedForwardTarget) Validate() error {
	if r.Purpose != "" || r.UDP || len(r.TransportGrants) != 0 {
		return errors.New("固定转发不能继承节点传输权限")
	}
	if err := r.validateDNS(); err != nil {
		return err
	}
	ip, err := HostAddress(r.Address)
	if err != nil || r.Port < 1 || r.Port > 65535 {
		return errors.New("固定转发端点无效")
	}
	if !NeedsTransportGrant(ip) {
		if len(r.Grants) != 0 {
			return errors.New("公网固定转发不能携带私网授权")
		}
		return r.ResolvedSOCKS5.Validate()
	}
	if ValidateForwardGrants(r.Grants) != nil || len(r.Grants) == 0 {
		return errors.New("私网固定转发缺少独立本机授权")
	}
	owner := r.Grants[0]
	for _, g := range r.Grants {
		if !g.Matches(owner.ForwardID, owner.EgressProfileID, ip, r.Port) {
			return errors.New("私网固定转发授权不匹配")
		}
	}
	return nil
}

func (r ResolvedForwardTarget) ValidateForForward(id, profile int64) error {
	if err := r.Validate(); err != nil {
		return err
	}
	for _, g := range r.Grants {
		if g.ForwardID != id || g.EgressProfileID != profile {
			return errors.New("固定转发使用了其他资源的授权")
		}
	}
	return nil
}

func (r ResolvedForwardTarget) WithGrants(id, profile int64, grants []ForwardGrant) (ResolvedForwardTarget, error) {
	r.Grants = nil
	if err := ValidateForwardGrants(grants); err != nil {
		return r, err
	}
	ip, err := HostAddress(r.Address)
	if err != nil {
		return r, err
	}
	if NeedsTransportGrant(ip) {
		for _, g := range ForwardGrantsFor(grants, id, profile) {
			if g.Matches(id, profile, ip, r.Port) {
				r.Grants = append(r.Grants, g)
			}
		}
	}
	return r, r.ValidateForForward(id, profile)
}

func (r ResolvedForwardTarget) StillAuthorized(grants []ForwardGrant) bool {
	if r.Validate() != nil || ValidateForwardGrants(grants) != nil {
		return false
	}
	for _, applied := range r.Grants {
		found := false
		for _, current := range grants {
			found = found || current == applied
		}
		if !found {
			return false
		}
	}
	return true
}
