package networkguard

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
)

// Existing node plans retain their JSON and lease names. Forward IDs live in
// another namespace; a node's readiness can never renew the matching forward.
func (b Binding) Resource() agentproto.ResourceIdentity {
	if b.ForwardID != 0 {
		return agentproto.ResourceIdentity{Kind: "forward", ID: b.ForwardID}
	}
	return agentproto.ResourceIdentity{Kind: "node", ID: b.NodeID}
}

func ResourceSetName(r agentproto.ResourceIdentity) string {
	prefix := "n"
	if r.Kind == "forward" {
		prefix = "f"
	}
	return fmt.Sprintf("%s%d_ready", prefix, r.ID)
}

// ParseLeaseSetName only recognizes canonical, bounded resource identities.
// The watchdog must ignore dynamic elements only in these exact lease sets.
func ParseLeaseSetName(name string) (agentproto.ResourceIdentity, bool) {
	var r agentproto.ResourceIdentity
	if len(name) < 8 || !strings.HasSuffix(name, "_ready") {
		return r, false
	}
	switch name[0] {
	case 'n':
		r.Kind = "node"
	case 'f':
		r.Kind = "forward"
	default:
		return r, false
	}
	var err error
	r.ID, err = strconv.ParseInt(strings.TrimSuffix(name[1:], "_ready"), 10, 64)
	if err != nil {
		return r, false
	}
	_, err = r.Mark()
	return r, err == nil && ResourceSetName(r) == name
}

func (b Binding) validateForward() error {
	if b.ForwardID == 0 {
		if b.Forward != nil || b.Applied.ForwardTarget != nil || b.ForwardTransport || b.ForwardFamily != "" {
			return errors.New("node guard cannot carry forward policy")
		}
		return nil
	}
	if b.Forward == nil || b.NodeID != 0 || b.Core != "singbox" || b.Group != "" {
		return errors.New("forward guard requires an independent singbox direct resource")
	}
	if b.ForwardTransport && b.Forward.EgressProfileID == 0 {
		return errors.New("固定转发中转缺少出口身份")
	}
	if b.ForwardTransport && b.ForwardFamily != "dual" && b.ForwardFamily != "ipv4" && b.ForwardFamily != "ipv6" || !b.ForwardTransport && b.ForwardFamily != "" {
		return errors.New("固定转发业务地址族无效")
	}
	if err := b.Forward.Validate(); err != nil {
		return err
	}
	if b.ListenPort != b.Forward.ListenPort || !reflect.DeepEqual(b.Wanted, b.Forward.BindingPolicy()) {
		return errors.New("forward guard binding differs from its fixed listener")
	}
	// Pending intent can fence unsupported candidates, but cannot renew them.
	if b.Pending {
		return nil
	}
	if b.ForwardTransport != (b.Applied.SOCKS5 != nil) {
		return errors.New("固定转发出口类型与本机端点不一致")
	}
	if pin := b.Applied.ForwardTarget; pin != nil {
		if err := pin.ValidateForForward(b.ForwardID, b.Forward.EgressProfileID); err != nil {
			return err
		}
	}
	ip, err := b.Forward.ResolvedTarget(b.Applied.ForwardTarget)
	if err != nil {
		return err
	}
	family := "dual"
	if b.ForwardTransport {
		family = b.ForwardFamily
	} else if b.Applied.Direct != nil {
		family = b.Applied.Direct.Config.Family
	}
	if ((b.IPv4Only || family == "ipv4") && !ip.Is4()) || (family == "ipv6" && ip.Is4()) {
		return errors.New("forward target conflicts with the applied address family")
	}
	return nil
}

func nodeReadiness(in map[int64]bool) map[agentproto.ResourceIdentity]bool {
	out := make(map[agentproto.ResourceIdentity]bool, len(in))
	for id, v := range in {
		out[agentproto.ResourceIdentity{Kind: "node", ID: id}] = v
	}
	return out
}

// Node-only adapters preserve old callers without broadening their authority.
func (p Plan) Healthy(s *agentproto.NetworkSnapshot, now time.Time) (map[int64]bool, map[int64]string) {
	ready, failures := p.HealthyResources(s, now)
	nodes, bad := map[int64]bool{}, map[int64]string{}
	for r, v := range ready {
		if r.Kind == "node" {
			nodes[r.ID] = v
		}
	}
	for r, v := range failures {
		if r.Kind == "node" {
			bad[r.ID] = v
		}
	}
	return nodes, bad
}

func (p Plan) Affected(indices map[int]bool, lost bool) map[int64]bool {
	out := map[int64]bool{}
	for r, v := range p.AffectedResources(indices, lost) {
		if r.Kind == "node" {
			out[r.ID] = v
		}
	}
	return out
}

func (p Plan) Renew(ready map[int64]bool) (string, error) {
	return p.RenewResources(nodeReadiness(ready))
}
func (p Plan) Revoke(affected map[int64]bool) (string, error) {
	return p.RevokeResources(nodeReadiness(affected))
}
