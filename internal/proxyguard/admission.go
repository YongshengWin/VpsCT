package proxyguard

import (
	"context"
	"errors"
	"strings"

	"ctlvps/internal/networkguard"
)

func emptyNetworkPlan() networkguard.Plan { return networkguard.Plan{Token: strings.Repeat("0", 32)} }

func liveAdmissionDigest(ctx context.Context) (string, error) {
	raw, err := command(ctx, "", "nft", "-j", "list", "table", "inet", networkguard.AdmissionTable)
	if err != nil {
		return "", err
	}
	// Reuse declaration-order normalization, but never strip connlimit rules.
	return networkDigest(raw)
}

func (p policy) hasAdmission() bool {
	return p.AdmissionDigest != "" || p.AdmissionReset || (p.Network != nil && p.Network.HasForwards())
}

// repairAdmission never silently forgets live admissions. Persisting reset
// intent first makes a crash after stopping/rebuilding safely repeatable.
func repairAdmission(ctx context.Context, p *policy) error {
	if !p.hasAdmission() {
		return nil
	}
	live, err := liveAdmissionDigest(ctx)
	if !p.AdmissionReset && err == nil && p.AdmissionDigest != "" && live == p.AdmissionDigest {
		return nil
	}
	p.AdmissionReset, p.AdmissionStopped = true, true
	if err := writePolicy(*p); err != nil {
		return err
	}
	if err := stopForwardCore(ctx); err != nil {
		return err
	}
	plan := emptyNetworkPlan()
	if p.Network != nil {
		plan = *p.Network
	}
	// Recreate admission and empty network leases in the same transaction.
	// The agent must observe an inactive core and apply it before traffic runs.
	rules, err := plan.AdmissionRules(emptyNetworkPlan(), true)
	if err != nil {
		return err
	}
	fence, err := plan.Rules()
	if err != nil {
		return err
	}
	if _, err := command(ctx, rules+fence, "nft", "-f", "-"); err != nil {
		return err
	}
	p.AdmissionDigest, err = liveAdmissionDigest(ctx)
	if err != nil {
		return err
	}
	p.NetworkDigest, err = liveNetworkDigest(ctx)
	if err != nil {
		return err
	}
	p.AdmissionReset = false
	return writePolicy(*p)
}

func stopForwardCore(ctx context.Context) error {
	units, err := activeUnits(ctx)
	if err != nil {
		return err
	}
	for _, unit := range units {
		if unit == "ctlvps-singbox.service" {
			return stopUnits(ctx, []string{unit})
		}
	}
	return nil
}

// Called under the policy lock, before publishing the next plan. A fresh table
// for the first forward has no old forward sockets to forget. Subsequent drift
// and changed limits must terminate the old process before resetting state.
func admissionTransition(ctx context.Context, p *policy, next networkguard.Plan) (string, error) {
	if !p.hasAdmission() && !next.HasForwards() {
		return "", nil
	}
	old := emptyNetworkPlan()
	if p.Network != nil {
		old = *p.Network
	}
	rebuild := !p.hasAdmission()
	if p.hasAdmission() {
		if err := repairAdmission(ctx, p); err != nil {
			return "", err
		}
	}
	if next.AdmissionNeedsStop(old) {
		p.AdmissionReset, p.AdmissionStopped = true, true
		if err := writePolicy(*p); err != nil {
			return "", err
		}
		if err := stopForwardCore(ctx); err != nil {
			return "", err
		}
	}
	rules, err := next.AdmissionRules(old, rebuild)
	if err != nil {
		return "", err
	}
	if rules == "" {
		return "", errors.New("missing forward admission policy")
	}
	return rules, nil
}

// ForwardCoreApplied acknowledges only the current root transaction after
// the agent has successfully applied the shared public service. A watchdog
// recovery alone cannot resume the stopped core or declare an application.
func ForwardCoreApplied(ctx context.Context, token string) error {
	return locked(ctx, func() error {
		p, err := readPolicy()
		if err != nil {
			return err
		}
		if p.Network == nil || p.Network.Token != token || p.AdmissionReset {
			return errors.New("forward application changed or admission recovery incomplete")
		}
		if !p.AdmissionStopped {
			return nil
		}
		live := false
		for _, b := range p.Network.Bindings {
			if b.Forward != nil && !b.Pending {
				live = true
			}
		}
		if live {
			units, err := activeUnits(ctx)
			if err != nil {
				return err
			}
			active := false
			for _, unit := range units {
				active = active || unit == "ctlvps-singbox.service"
			}
			if !active {
				return errors.New("shared forwarding core has not been applied")
			}
		}
		p.AdmissionStopped = false
		return writePolicy(p)
	})
}
