package netinventory

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

// ResolveBinding is an application preflight, not a substitute for continuous
// guard enforcement or a destination-specific route check. It only accepts a
// fresh complete local sample; callers must not use controller-cached inventory.
func ResolveBinding(n networkconfig.Node, direct *networkconfig.Direct, snapshot *agentproto.NetworkSnapshot, ipv4Only bool, now time.Time) (networkconfig.Resolved, error) {
	if snapshot == nil || now.Sub(snapshot.SampledAt) > 5*time.Second || snapshot.SampledAt.Sub(now) > time.Second {
		return networkconfig.Resolved{}, errors.New("应用网络绑定需要新鲜、完整的本机接口清单")
	}
	return resolveBindingSelection(n, direct, snapshot, ipv4Only)
}

// ValidateBindingSelection checks a controller-side selection against the
// reported inventory without manufacturing a fresh local sample. The caller
// must separately check when the controller received that inventory. No runtime
// indices, names or resolved candidate cross this preview boundary.
func ValidateBindingSelection(n networkconfig.Node, direct *networkconfig.Direct, snapshot *agentproto.NetworkSnapshot, ipv4Only bool) error {
	_, err := resolveBindingSelection(n, direct, snapshot, ipv4Only)
	return err
}

func resolveBindingSelection(n networkconfig.Node, direct *networkconfig.Direct, snapshot *agentproto.NetworkSnapshot, ipv4Only bool) (networkconfig.Resolved, error) {
	var out networkconfig.Resolved
	if err := n.Validate(); err != nil {
		return out, err
	}
	if (direct == nil) != (n.EgressProfileID == 0) {
		return out, errors.New("出口引用与已解析配置不一致")
	}
	if snapshot == nil || agentproto.ValidateNetwork(snapshot) != nil || snapshot.Status != "ok" {
		return out, errors.New("网络绑定需要完整有效的接口清单")
	}
	out.CollectorID, out.BootID, out.Sequence = snapshot.CollectorID, snapshot.BootID, snapshot.Sequence
	interfaces := map[string]agentproto.NetworkInterface{}
	for _, iface := range snapshot.Interfaces {
		interfaces[iface.ID] = iface
	}
	lookup := func(id string) (networkconfig.Interface, error) {
		i, ok := interfaces[id]
		if !ok {
			return networkconfig.Interface{}, errors.New("绑定的接口已不存在，请重新选择")
		}
		if !i.Up || !i.Carrier || i.Kind == "loopback" {
			return networkconfig.Interface{}, errors.New("绑定的接口未就绪")
		}
		return networkconfig.Interface{ID: i.ID, Name: i.Name, Index: i.Index}, nil
	}
	address := func(id, raw string) (networkconfig.Interface, error) {
		i, err := lookup(id)
		if err != nil {
			return i, err
		}
		wanted, err := networkconfig.HostAddress(raw)
		if err != nil {
			return i, err
		}
		if ipv4Only && n.IngressFamily != "ipv6" && !wanted.Is4() {
			return i, errors.New("服务器仅 IPv4 设置与所选地址冲突")
		}
		owner := ""
		for _, candidate := range snapshot.Interfaces {
			for _, rawPrefix := range candidate.Addresses {
				prefix, err := netip.ParsePrefix(rawPrefix)
				if err == nil && prefix.Addr().Unmap() == wanted {
					if owner != "" && owner != candidate.ID {
						return i, errors.New("该地址出现在多个接口上，无法唯一确定绑定")
					}
					owner = candidate.ID
				}
			}
		}
		if owner != id {
			return i, errors.New("地址已不属于选定接口")
		}
		ready := false
		for _, rawPrefix := range interfaces[id].UsableAddresses {
			prefix, err := netip.ParsePrefix(rawPrefix)
			ready = ready || (err == nil && prefix.Addr().Unmap() == wanted)
		}
		if !ready {
			return i, errors.New("地址尚未完成校验或已失效，暂不能绑定")
		}
		return i, nil
	}
	out.ListenAddress = "::"
	if ipv4Only {
		out.ListenAddress = "0.0.0.0"
	}
	if n.ListenMode == "address" {
		i, err := address(n.ListenInterfaceID, n.ListenAddress)
		if err != nil {
			return networkconfig.Resolved{}, fmt.Errorf("监听地址：%w", err)
		}
		a, _ := networkconfig.HostAddress(n.ListenAddress)
		if n.IngressFamily == "ipv6" {
			for _, raw := range interfaces[i.ID].TemporaryIPv6 {
				p, _ := netip.ParsePrefix(raw)
				if p.Addr() == a {
					return networkconfig.Resolved{}, errors.New("仅 IPv6 节点不能绑定临时 IPv6 地址")
				}
			}
		}
		out.ListenAddress, out.ListenInterface = a.String(), &i
	}
	if n.ListenMode == "public_ipv6" {
		ip, err := PublicIPv6Candidate(snapshot, n.ListenInterfaceID)
		if err != nil {
			return networkconfig.Resolved{}, err
		}
		i, err := address(ip.InterfaceID, ip.Address)
		if err != nil {
			return networkconfig.Resolved{}, err
		}
		out.ListenAddress, out.ListenInterface = ip.Address, &i
	}
	if direct == nil {
		return out, nil
	}
	cfg := *direct
	if ipv4Only {
		if cfg.Family == "ipv6" {
			return networkconfig.Resolved{}, errors.New("IPv6 出口与服务器仅 IPv4 设置冲突")
		}
		// Apply the server's stricter policy, including DNS and source fields.
		if cfg.Family == "dual" {
			cfg.Family = "ipv4"
		}
	}
	if err := cfg.Validate(); err != nil {
		return networkconfig.Resolved{}, err
	}
	rd := &networkconfig.ResolvedDirect{Config: cfg}
	owners := map[string]networkconfig.Interface{}
	if cfg.InterfaceID != "" {
		i, err := lookup(cfg.InterfaceID)
		if err != nil {
			return networkconfig.Resolved{}, fmt.Errorf("出口：%w", err)
		}
		rd.Interface, owners[i.ID] = &i, i
	}
	for _, src := range []*networkconfig.Address{cfg.SourceIPv4, cfg.SourceIPv6} {
		if src == nil {
			continue
		}
		i, err := address(src.InterfaceID, src.Address)
		if err != nil {
			return networkconfig.Resolved{}, fmt.Errorf("出口源地址：%w", err)
		}
		owners[i.ID] = i
	}
	for _, i := range owners {
		rd.Owners = append(rd.Owners, i)
	}
	sort.Slice(rd.Owners, func(i, j int) bool { return rd.Owners[i].ID < rd.Owners[j].ID })
	// Copy pointed-to values so future edits of an input cannot mutate the
	// already validated application candidate.
	if cfg.SourceIPv4 != nil {
		copy := *cfg.SourceIPv4
		rd.Config.SourceIPv4 = &copy
	}
	if cfg.SourceIPv6 != nil {
		copy := *cfg.SourceIPv6
		rd.Config.SourceIPv6 = &copy
	}
	out.Direct = rd
	return out, nil
}

// PublicIPv6Candidate refuses to guess when a host has more than one eligible
// address. A selected interface can disambiguate multi-homed hosts.
func PublicIPv6Candidate(snapshot *agentproto.NetworkSnapshot, interfaceID string) (networkconfig.Address, error) {
	var result networkconfig.Address
	if snapshot != nil && snapshot.Status == "ok" {
		for _, iface := range snapshot.Interfaces {
			if !iface.Up || !iface.Carrier || iface.Kind == "loopback" || interfaceID != "" && iface.ID != interfaceID {
				continue
			}
			for _, raw := range iface.UsableAddresses {
				p, err := netip.ParsePrefix(raw)
				if err != nil || !networkconfig.IsPublicIPv6(p.Addr().String()) || slices.Contains(iface.TemporaryIPv6, raw) {
					continue
				}
				candidate := networkconfig.Address{InterfaceID: iface.ID, Address: p.Addr().String()}
				if result == candidate {
					continue
				}
				if result.Address != "" {
					return networkconfig.Address{}, errors.New("有多个公网 IPv6 地址，请指定监听接口或具体地址")
				}
				result = candidate
			}
		}
	}
	if result.Address == "" {
		return result, errors.New("监听接口没有可用的公网 IPv6")
	}
	return result, nil
}
