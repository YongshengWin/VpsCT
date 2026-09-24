package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"ctlvps/internal/agentproto"
)

var ErrBillingConflict = errors.New("计费策略已变化，请刷新后重试")

type NetworkBillingView struct {
	Current   agentproto.NetworkBillingPolicy `json:"current"`
	Requested agentproto.NetworkBillingPolicy `json:"requested"`
	Status    string                          `json:"status"`
	Error     string                          `json:"error"`
	AppliedAt *time.Time                      `json:"applied_at"`
	SampledAt *time.Time                      `json:"sampled_at"`
}

func BillingPolicy(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, serverID, revision int64) (agentproto.NetworkBillingPolicy, error) {
	if revision == 0 {
		return agentproto.LegacyNetworkBilling(), nil
	}
	var p agentproto.NetworkBillingPolicy
	var raw string
	if err := q.QueryRowContext(ctx, "SELECT policy FROM network_billing_policies WHERE server_id=? AND revision=?", serverID, revision).Scan(&raw); err != nil {
		return p, err
	}
	err := json.Unmarshal([]byte(raw), &p)
	return p, err
}

func (s *Store) NetworkBilling(ctx context.Context, serverID int64) (NetworkBillingView, error) {
	v := NetworkBillingView{Current: agentproto.LegacyNetworkBilling(), Requested: agentproto.LegacyNetworkBilling(), Status: "legacy"}
	var current, requested int64
	var applied, sampled sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT current_revision,requested_revision,status,error,applied_at,sampled_at FROM network_billing_state WHERE server_id=?", serverID).Scan(&current, &requested, &v.Status, &v.Error, &applied, &sampled)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Current, err = BillingPolicy(ctx, s.db, serverID, current)
	if err != nil {
		return v, err
	}
	v.Requested, err = BillingPolicy(ctx, s.db, serverID, requested)
	if err != nil {
		return v, err
	}
	if applied.Valid {
		at := parseTime(applied.String)
		v.AppliedAt = &at
	}
	if sampled.Valid {
		at := parseTime(sampled.String)
		v.SampledAt = &at
	}
	return v, nil
}

func (s *Store) RequestNetworkBilling(ctx context.Context, serverID, expected int64, policy agentproto.NetworkBillingPolicy) (agentproto.NetworkBillingPolicy, error) {
	if err := policy.Validate(); err != nil {
		return policy, err
	}
	sort.Strings(policy.InterfaceIDs)
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO network_billing_state(server_id) VALUES (?) ON CONFLICT DO NOTHING", serverID); err != nil {
			return err
		}
		var revision int64
		if err := tx.QueryRowContext(ctx, "SELECT requested_revision FROM network_billing_state WHERE server_id=?", serverID).Scan(&revision); err != nil {
			return err
		}
		if expected != revision {
			return ErrBillingConflict
		}
		if revision >= 4096 {
			return errors.New("计费策略版本已达到容量上限")
		}
		policy.Revision = revision + 1
		b, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO network_billing_policies(server_id,revision,policy,created_at) VALUES (?,?,?,?)", serverID, policy.Revision, string(b), fmtTime(s.Now())); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE network_billing_state SET requested_revision=? WHERE server_id=?", policy.Revision, serverID)
		return err
	})
	return policy, err
}

// ValidateBillingInterfaces rejects known double-counting relationships.
// Selecting a layer is explicit; default routes are not billing boundaries.
func ValidateBillingInterfaces(snapshot *agentproto.NetworkSnapshot, policy agentproto.NetworkBillingPolicy) error {
	return agentproto.ValidateBillingSelection(snapshot, policy)
}
