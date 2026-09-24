package store

import (
	"context"
	"database/sql"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

// DesiredForwards resolves each immutable direct revision in the same SQLite
// snapshot as its consumer. Missing references fail; never fall back to direct.
func (s *Store) DesiredForwards(ctx context.Context, serverID int64) ([]agentproto.ForwardSpec, error) {
	var out []agentproto.ForwardSpec
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+forwardCols+` FROM port_forwards WHERE server_id=? ORDER BY id`, serverID)
		if err != nil {
			return err
		}
		var forwards []domain.PortForward
		for rows.Next() {
			f, err := scanForward(rows)
			if err != nil {
				rows.Close()
				return err
			}
			forwards = append(forwards, f)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, f := range forwards {
			spec := agentproto.ForwardSpec{ForwardID: f.ID, Revision: f.Revision, Config: f.Config, Blocked: !f.Enabled || f.Retired, Retired: f.Retired}
			if f.Config.EgressProfileID != 0 {
				var raw, kind string
				var enabled bool
				err := tx.QueryRowContext(ctx, `SELECT p.kind,p.enabled,r.config FROM egress_profiles p JOIN egress_profile_revisions r ON r.profile_id=p.id
 WHERE p.id=? AND p.server_id=? AND r.revision=? AND r.server_id=p.server_id`, f.Config.EgressProfileID, serverID, f.Config.EgressRevision).Scan(&kind, &enabled, &raw)
				if err != nil {
					return err
				}
				spec.Blocked = spec.Blocked || !enabled
				switch kind {
				case "direct":
					cfg, err := networkconfig.DecodeDirect([]byte(raw))
					if err != nil {
						return err
					}
					spec.Direct = &cfg
				case "socks5":
					cfg, err := networkconfig.DecodeSOCKS5([]byte(raw))
					if err != nil {
						return err
					}
					secret := networkconfig.SOCKS5Credentials{}
					if cfg.Authentication == "password" {
						secret, err = s.egressCredentials(ctx, tx, serverID, f.Config.EgressProfileID, f.Config.EgressRevision)
						if err != nil {
							return err
						}
					}
					spec.SOCKS5 = &agentproto.SOCKS5Egress{Config: cfg, Credentials: secret}
				case "ss2022":
					cfg, err := networkconfig.DecodeSS2022([]byte(raw))
					if err != nil {
						return err
					}
					secret, err := s.egressCredentials(ctx, tx, serverID, f.Config.EgressProfileID, f.Config.EgressRevision)
					if err != nil {
						return err
					}
					spec.SS2022 = &agentproto.SS2022Egress{Config: cfg, Credentials: secret}
				case "ssh":
					cfg, err := networkconfig.DecodeSSH([]byte(raw))
					if err != nil {
						return err
					}
					secret, err := s.egressCredentials(ctx, tx, serverID, f.Config.EgressProfileID, f.Config.EgressRevision)
					if err != nil {
						return err
					}
					spec.SSH = &agentproto.SSHEgress{Config: cfg, Credentials: secret}
				case "wireguard":
					cfg, err := networkconfig.DecodeWireGuard([]byte(raw))
					if err != nil {
						return err
					}
					secret, err := s.egressCredentials(ctx, tx, serverID, f.Config.EgressProfileID, f.Config.EgressRevision)
					if err != nil {
						return err
					}
					spec.WireGuard = &agentproto.WireGuardEgress{Config: cfg, Credentials: secret}
				default:
					return ErrNetworkConflict
				}
			}
			if err := spec.Validate(); err != nil {
				return err
			}
			out = append(out, spec)
		}
		return nil
	})
	return out, err
}
