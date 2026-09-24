package agentproto

import (
	"ctlvps/internal/networkconfig"
	"errors"
	"math"
	"slices"
	"time"
)

const NetworkBillingVersion = networkconfig.BillingVersion

type NetworkBillingPolicy struct {
	Revision     int64    `json:"revision"`
	Mode         string   `json:"mode"` // legacy | interfaces
	InterfaceIDs []string `json:"interface_ids"`
}

func LegacyNetworkBilling() NetworkBillingPolicy {
	return NetworkBillingPolicy{Mode: "legacy", InterfaceIDs: []string{}}
}

func (p NetworkBillingPolicy) Validate() error {
	if p.Revision < 0 || p.Revision > 1<<53 || (p.Mode != "legacy" && p.Mode != "interfaces") {
		return errors.New("计费策略格式无效")
	}
	if (p.Mode == "legacy" && len(p.InterfaceIDs) != 0) || (p.Mode == "interfaces" && (len(p.InterfaceIDs) == 0 || len(p.InterfaceIDs) > 16)) {
		return errors.New("请选择 1–16 张计费网卡")
	}
	seen := map[string]bool{}
	for _, id := range p.InterfaceIDs {
		if !networkIdentity.MatchString(id) || seen[id] {
			return errors.New("计费网卡身份无效或重复")
		}
		seen[id] = true
	}
	return nil
}

func (p NetworkBillingPolicy) Equal(q NetworkBillingPolicy) bool {
	return p.Revision == q.Revision && p.Mode == q.Mode && slices.Equal(p.InterfaceIDs, q.InterfaceIDs)
}

type LegacyNetworkCounters struct {
	Epoch string `json:"epoch"`
	Rx    int64  `json:"rx"`
	Tx    int64  `json:"tx"`
}

func (p LegacyNetworkCounters) Validate() error {
	if p.Epoch == "" || len(p.Epoch) > 256 || p.Rx < 0 || p.Tx < 0 || p.Rx > math.MaxInt64/4 || p.Tx > math.MaxInt64/4 {
		return errors.New("旧计费来源无效")
	}
	return nil
}

// NetworkBillingSwitch freezes both sides of a sampling boundary. The complete
// envelope is persisted by the agent, and retried unchanged until acknowledged.
type NetworkBillingSwitch struct {
	ID               string                `json:"id"`
	PreviousRevision int64                 `json:"previous_revision"`
	Next             NetworkBillingPolicy  `json:"next"`
	TS               time.Time             `json:"ts"`
	Legacy           LegacyNetworkCounters `json:"legacy"`
	Snapshot         *NetworkSnapshot      `json:"snapshot"`
}

func (p *NetworkBillingSwitch) Validate() error {
	if p == nil || !networkIdentity.MatchString(p.ID) || p.TS.IsZero() || p.PreviousRevision < 0 || p.Next.Revision <= p.PreviousRevision || p.Next.Validate() != nil {
		return errors.New("计费切换快照无效")
	}
	if p.Snapshot == nil || (p.Snapshot.Status != "ok" && p.Snapshot.Status != "incomplete") || ValidateNetwork(p.Snapshot) != nil {
		return errors.New("计费切换缺少有效网卡快照")
	}
	if err := p.Legacy.Validate(); err != nil {
		return err
	}
	return ValidateBillingSelection(p.Snapshot, p.Next)
}
