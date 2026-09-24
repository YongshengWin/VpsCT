// Package networkguard compiles fail-closed, leased network bindings. The
// network policy comes from the agent's locally resolved application candidate.
package networkguard

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"ctlvps/internal/agentbudget"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
)

const Table = "ctlvps_network"
const Lease = 6 * time.Second

type Binding struct {
	Pending          bool                   `json:"pending,omitempty"`     // never renewable, even when the interface is healthy
	Fingerprint      string                 `json:"fingerprint,omitempty"` // binds the applied node config, excluding secrets themselves
	NodeID           int64                  `json:"node_id"`
	ForwardID        int64                  `json:"forward_id,omitempty"`
	Forward          *networkconfig.Forward `json:"forward,omitempty"`
	ForwardTransport bool                   `json:"forward_transport,omitempty"`
	ForwardFamily    string                 `json:"forward_family,omitempty"`
	ListenPort       int                    `json:"listen_port"`
	Core             string                 `json:"core"`
	Group            string                 `json:"group,omitempty"` // local Snell cgroup
	Wanted           networkconfig.Node     `json:"wanted"`
	Applied          networkconfig.Resolved `json:"applied"`
	IPv4Only         bool                   `json:"ipv4_only"`
}

type Plan struct {
	Token    string    `json:"token"`
	Bindings []Binding `json:"bindings"`
}

func New(bindings []Binding) (Plan, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Plan{}, err
	}
	p := Plan{Token: hex.EncodeToString(token[:]), Bindings: bindings}
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	return p, nil
}

func SetName(id int64) string { return fmt.Sprintf("n%d_ready", id) }

func (p Plan) Validate() error {
	// An in-flight replacement may fence both retired and new identities.
	if !networkconfig.ValidIdentity(p.Token) || len(p.Bindings) > 2*(agentbudget.ActiveNodes+agentbudget.ActiveForwards) {
		return errors.New("invalid network guard plan")
	}
	ids := map[agentproto.ResourceIdentity]bool{}
	counts := map[string]int{}
	activePorts := map[int]bool{}
	for _, b := range p.Bindings {
		resource := b.Resource()
		if _, err := resource.Mark(); err != nil {
			return err
		}
		if b.NodeID != 0 && b.ForwardID != 0 {
			return errors.New("ambiguous network guard resource identity")
		}
		if err := b.validateForward(); err != nil {
			return err
		}
		if b.ListenPort < 1 || b.ListenPort > 65535 || ids[resource] {
			return errors.New("duplicate or invalid network guard identity")
		}
		ids[resource] = true
		counts[resource.Kind]++
		if counts["node"] > 2*agentbudget.ActiveNodes || counts["forward"] > 2*agentbudget.ActiveForwards {
			return errors.New("network guard resource budget exceeded")
		}
		if !b.Pending {
			if activePorts[b.ListenPort] {
				return errors.New("duplicate active network guard listener")
			}
			activePorts[b.ListenPort] = true
		}
		if b.Fingerprint != "" {
			v, err := hex.DecodeString(b.Fingerprint)
			if err != nil || len(v) != 32 {
				return errors.New("invalid network application fingerprint")
			}
		}
		if err := b.Wanted.Validate(); err != nil {
			return err
		}
		if b.Core != "singbox" && b.Core != "snell" && b.Core != "mieru" {
			return errors.New("unsupported network guard core")
		}
		if b.Core == "snell" || b.Core == "mieru" {
			want := fmt.Sprintf("ctlvps-proxy-n%d.slice", b.NodeID)
			if b.Wanted.EgressProfileID != 0 || b.Applied.Direct != nil || !strings.HasSuffix(b.Group, "/"+want) || !strings.HasPrefix(b.Group, "/ctlvps.slice/ctlvps-proxy.slice/") || strings.ContainsAny(b.Group, "\"\\\r\n\t ") || strings.Contains(b.Group, "..") {
				return errors.New("standalone guard requires its dedicated local cgroup and no custom outbound")
			}
		}
		if b.Pending {
			if !reflect.DeepEqual(b.Applied, networkconfig.Resolved{}) {
				return errors.New("pending network guard must not contain an applied observation")
			}
			continue
		}
		if !networkconfig.ValidIdentity(b.Applied.CollectorID) || b.Applied.BootID == "" || b.Applied.Sequence < 1 {
			return errors.New("network guard requires a local observation")
		}
		if b.Wanted.ListenMode == "address" {
			i := b.Applied.ListenInterface
			if !validInterface(i) || i.ID != b.Wanted.ListenInterfaceID {
				return errors.New("unresolved listener interface")
			}
			a, err := networkconfig.HostAddress(b.Wanted.ListenAddress)
			if err != nil || a.String() != b.Applied.ListenAddress {
				return errors.New("unresolved listener address")
			}
		} else if b.Applied.ListenInterface != nil || (b.Applied.ListenAddress != "::" && b.Applied.ListenAddress != "0.0.0.0") {
			return errors.New("invalid wildcard listener observation")
		}
		if (b.Wanted.EgressProfileID == 0) != (b.Applied.Direct == nil) {
			return errors.New("unresolved network guard outbound")
		}
		if d := b.Applied.Direct; d != nil {
			if err := d.Config.Validate(); err != nil {
				return err
			}
			if (d.Config.InterfaceID == "") != (d.Interface == nil) {
				return errors.New("unresolved outbound interface")
			}
			owners, indices := map[string]networkconfig.Interface{}, map[int]bool{}
			for _, owner := range d.Owners {
				if !validInterface(&owner) || indices[owner.Index] {
					return errors.New("invalid outbound owner")
				}
				if _, ok := owners[owner.ID]; ok {
					return errors.New("duplicate outbound owner")
				}
				owners[owner.ID], indices[owner.Index] = owner, true
			}
			if len(owners) > 3 {
				return errors.New("too many outbound owners")
			}
			if d.Interface != nil && (!validInterface(d.Interface) || d.Interface.ID != d.Config.InterfaceID || owners[d.Interface.ID] != *d.Interface) {
				return errors.New("outbound interface identity differs")
			}
			for _, src := range []*networkconfig.Address{d.Config.SourceIPv4, d.Config.SourceIPv6} {
				if src != nil {
					if _, ok := owners[src.InterfaceID]; !ok {
						return errors.New("unresolved source owner")
					}
				}
			}
		}
		if endpoint := b.Applied.SOCKS5; endpoint != nil {
			if b.Core != "singbox" || b.Applied.Direct == nil {
				return errors.New("SOCKS5 guard requires singbox and a resolved outer binding")
			}
			if err := endpoint.ValidateForBinding(resource.ID, b.Wanted.EgressProfileID); err != nil {
				return err
			}
			ip, _ := networkconfig.HostAddress(endpoint.Address)
			family := b.Applied.Direct.Config.Family
			if (family == "ipv4" && !ip.Is4()) || (family == "ipv6" && ip.Is4()) {
				return errors.New("SOCKS5 endpoint conflicts with outer address family")
			}
		}
	}
	return nil
}

func validInterface(i *networkconfig.Interface) bool {
	return i != nil && networkconfig.ValidIdentity(i.ID) && i.Index > 0 && i.Name != "" && len(i.Name) <= 15 && !strings.ContainsAny(i.Name, "/:\x00\r\n\t ")
}

// Healthy never silently recompiles a running process after a rename or reboot.
// The agent must apply a new locally resolved candidate before renewing leases.
func (p Plan) HealthyResources(snapshot *agentproto.NetworkSnapshot, now time.Time) (map[agentproto.ResourceIdentity]bool, map[agentproto.ResourceIdentity]string) {
	ready, failures := map[agentproto.ResourceIdentity]bool{}, map[agentproto.ResourceIdentity]string{}
	valid := p.Validate()
	for _, b := range p.Bindings {
		resource := b.Resource()
		if b.Pending {
			failures[resource] = "网络配置尚未成功应用"
			continue
		}
		if valid != nil {
			failures[resource] = "本机网络保护配置无效"
			continue
		}
		if snapshot == nil || now.Sub(snapshot.SampledAt) > time.Second {
			failures[resource] = "接口采样已过期，等待本机重新采集"
			continue
		}
		var direct *networkconfig.Direct
		if b.Applied.Direct != nil {
			direct = &b.Applied.Direct.Config
		}
		r, err := netinventory.ResolveBinding(b.Wanted, direct, snapshot, b.IPv4Only, now)
		if err != nil {
			failures[resource] = err.Error()
			continue
		}
		if endpoint := b.Applied.SOCKS5; endpoint != nil {
			if endpoint.DNSExpired(now) {
				failures[resource] = "上游域名解析已过期，等待本机重新解析"
				continue
			}
			if err := netinventory.ValidateSOCKS5Endpoint(*endpoint, snapshot); err != nil {
				failures[resource] = err.Error()
				continue
			}
			r.SOCKS5 = endpoint
		}
		if b.Forward != nil {
			if pin := b.Applied.ForwardTarget; pin != nil && pin.DNSExpired(now) {
				failures[resource] = "固定目标域名解析已过期"
				continue
			}
			if err := netinventory.ValidateResolvedForwardTarget(*b.Forward, b.Applied.ForwardTarget, snapshot); err != nil {
				failures[resource] = err.Error()
				continue
			}
		}
		r.ForwardTarget = b.Applied.ForwardTarget
		if r.CollectorID != b.Applied.CollectorID || r.BootID != b.Applied.BootID || r.Sequence < b.Applied.Sequence {
			failures[resource] = "接口观测身份已变化，等待重新应用"
			continue
		}
		// Sequence advances during normal sampling and is not part of binding
		// identity. Everything else must still match the applied process.
		r.Sequence = b.Applied.Sequence
		if !reflect.DeepEqual(r, b.Applied) {
			failures[resource] = "绑定接口或地址已变化，等待重新应用"
			continue
		}
		ready[resource] = true
	}
	return ready, failures
}

// SamePaths permits only renewed DNS timing. A changed endpoint, credential
// fingerprint, observation identity, pending state or binding needs a new plan.
func (p Plan) SamePaths(next Plan) bool {
	strip := func(list []Binding) []Binding {
		for i := range list {
			if pin := list[i].Applied.ForwardTarget; pin != nil {
				copy := pin.WithoutDNSLifetime()
				list[i].Applied.ForwardTarget = &copy
			}
			if pin := list[i].Applied.SOCKS5; pin != nil {
				copy := pin.WithoutDNSLifetime()
				list[i].Applied.SOCKS5 = &copy
			}
		}
		return list
	}
	return reflect.DeepEqual(strip(p.sorted()), strip(next.sorted()))
}

func (p Plan) AffectedResources(indices map[int]bool, lost bool) map[agentproto.ResourceIdentity]bool {
	out := map[agentproto.ResourceIdentity]bool{}
	for _, b := range p.Bindings {
		affected := lost || (b.Applied.ListenInterface != nil && indices[b.Applied.ListenInterface.Index])
		if b.Applied.Direct != nil {
			for _, owner := range b.Applied.Direct.Owners {
				affected = affected || indices[owner.Index]
			}
		}
		if affected {
			out[b.Resource()] = true
		}
	}
	return out
}

func (p Plan) sorted() []Binding {
	out := append([]Binding(nil), p.Bindings...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Resource(), out[j].Resource()
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	return out
}
