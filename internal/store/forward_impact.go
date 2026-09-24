package store

import (
	"context"

	"ctlvps/internal/domain"
)

type NetworkImpactForward struct {
	ForwardID       int64  `json:"forward_id"`
	Name            string `json:"name"`
	Revision        int64  `json:"revision"`
	ListenPort      int    `json:"listen_port"`
	Enabled         bool   `json:"enabled"`
	Retired         bool   `json:"retired"`
	EgressProfileID int64  `json:"egress_profile_id,omitempty"`
	EgressRevision  int64  `json:"egress_revision,omitempty"`
	EgressEnabled   bool   `json:"egress_enabled"`
	Effect          string `json:"effect"`
	RestartPossible bool   `json:"restart_possible"`
}

func (s *Store) impactForwards(ctx context.Context, q querier, serverID int64) ([]NetworkImpactForward, error) {
	rows, err := q.QueryContext(ctx, `SELECT f.id,f.name,f.revision,f.listen_port,f.enabled,f.retired,COALESCE(f.egress_profile_id,0),COALESCE(f.egress_revision,0),COALESCE(p.enabled,1)
 FROM port_forwards f LEFT JOIN egress_profiles p ON p.id=f.egress_profile_id WHERE f.server_id=? ORDER BY f.id LIMIT ?`, serverID, MaxPortForwards+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetworkImpactForward
	for rows.Next() {
		var f NetworkImpactForward
		if err := rows.Scan(&f.ForwardID, &f.Name, &f.Revision, &f.ListenPort, &f.Enabled, &f.Retired, &f.EgressProfileID, &f.EgressRevision, &f.EgressEnabled); err != nil {
			return nil, err
		}
		out = append(out, f)
		if len(out) > MaxPortForwards {
			return nil, ErrNetworkImpactCapacity
		}
	}
	return out, rows.Err()
}

func addForwardRestarts(v *NetworkImpact, forwards []NetworkImpactForward) {
	for _, f := range forwards {
		if f.Retired {
			continue
		}
		f.Effect, f.RestartPossible = "restart", true
		v.Forwards = append(v.Forwards, f)
		v.RestartCount++
	}
}

// Ordinary node edits can restart the shared process containing independent
// forwards. Include both kinds in review hashes without exposing target secrets.
func (s *Store) addNodeForwardImpact(ctx context.Context, q querier, v *NetworkImpact, core domain.Core) error {
	if core != domain.CoreSingBox {
		return nil
	}
	forwards, err := s.impactForwards(ctx, q, v.ServerID)
	if err != nil {
		return err
	}
	addForwardRestarts(v, forwards)
	return nil
}
