package proxyguard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/udpsession"
)

// The UDP worker cannot grant general networking readiness. A short separate
// lease gates client UDP only; TCP and shared tunnel transports retain theirs.
func ForwardUDPLease(ctx context.Context, policies map[int64]udpsession.Policy, healthy bool) error {
	return locked(ctx, func() error {
		p, err := readPolicy()
		if err != nil {
			return err
		}
		if p.Network == nil || p.Network.UDPVersion != networkconfig.ForwardUDPVersion {
			return errors.New("UDP network policy unavailable")
		}
		if err = repairNetwork(ctx, &p); err != nil {
			return err
		}
		var s strings.Builder
		matched := 0
		for _, b := range p.Network.Bindings {
			policy, ok := policies[b.ForwardID]
			if !ok {
				continue
			}
			if b.Forward == nil || b.Forward.MaxUDPSessions == 0 || b.ListenPort != int(policy.ListenPort) || b.Forward.UDPIdleSeconds != int(policy.Idle.Seconds()) {
				return errors.New("UDP application changed")
			}
			matched++
			name := networkguard.UDPSetName(b.ForwardID)
			fmt.Fprintf(&s, "flush set inet %s %s\n", networkguard.Table, name)
			if healthy && (p.AdmissionStopped || p.AdmissionReset) {
				return errors.New("UDP admission awaiting application")
			}
			if healthy && !b.Pending {
				fmt.Fprintf(&s, "add element inet %s %s { ipv4 timeout 6s, ipv6 timeout 6s }\n", networkguard.Table, name)
			}
		}
		if matched != len(policies) {
			return errors.New("UDP resource no longer current")
		}
		if s.Len() == 0 {
			return nil
		}
		_, err = command(ctx, s.String(), "nft", "-f", "-")
		return err
	})
}
