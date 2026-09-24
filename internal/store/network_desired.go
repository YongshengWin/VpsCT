package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

type DeployedNodeNetwork struct {
	Node        domain.Node
	Profile     *domain.EgressProfile
	Revision    *domain.EgressRevision
	Credentials *networkconfig.SOCKS5Credentials `json:"-"`
}

// DeployedNodeNetworks reads bindings and their immutable referenced revisions
// in one SQLite snapshot. Concurrent detach/delete cannot mix two generations
// or turn a missing revision into a silent direct fallback.
func (s *Store) DeployedNodeNetworks(ctx context.Context, serverID int64) ([]DeployedNodeNetwork, error) {
	out := []DeployedNodeNetwork{}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE server_id=? AND source IN ('deployed','transit') ORDER BY sort_order,id`, serverID)
		if err != nil {
			return err
		}
		for rows.Next() {
			n, err := s.scanNode(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, DeployedNodeNetwork{Node: n})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		type key struct{ id, revision int64 }
		profiles := map[int64]*domain.EgressProfile{}
		revisions := map[key]*domain.EgressRevision{}
		credentials := map[key]*networkconfig.SOCKS5Credentials{}
		for i, item := range out {
			n := item.Node
			if n.Revoked || n.Network == nil || n.Network.EgressProfileID == 0 {
				continue
			}
			id, revision := n.Network.EgressProfileID, n.Network.EgressRevision
			if profiles[id] == nil {
				p, err := scanEgress(tx.QueryRowContext(ctx, `SELECT `+egressCols+` FROM egress_profiles WHERE id=? AND server_id=?`, id, serverID))
				if err != nil {
					return err
				}
				profiles[id] = &p
			}
			k := key{id, revision}
			if revisions[k] == nil {
				var config, created string
				if err := tx.QueryRowContext(ctx, `SELECT config,created_at FROM egress_profile_revisions WHERE profile_id=? AND revision=? AND server_id=?`, id, revision, serverID).Scan(&config, &created); err != nil {
					return err
				}
				revisions[k] = &domain.EgressRevision{ProfileID: id, ServerID: serverID, Revision: revision, Config: json.RawMessage(config), CreatedAt: parseTime(created)}
				if profiles[id].Kind == "ssh" || profiles[id].Kind == "wireguard" || profiles[id].Kind == "ss2022" {
					secret, err := s.egressCredentials(ctx, tx, serverID, id, revision)
					if err != nil {
						return err
					}
					credentials[k] = &secret
					revisions[k].HasCredentials = true
				}
				if profiles[id].Kind == "socks5" {
					cfg, err := networkconfig.DecodeSOCKS5(revisions[k].Config)
					if err != nil {
						return err
					}
					if cfg.Authentication == "password" {
						secret, err := s.egressCredentials(ctx, tx, serverID, id, revision)
						if err != nil {
							return err
						}
						credentials[k] = &secret
						revisions[k].HasCredentials = true
					}
				}
			}
			out[i].Profile, out[i].Revision = profiles[id], revisions[k]
			out[i].Credentials = credentials[k]
		}
		return nil
	})
	return out, err
}
