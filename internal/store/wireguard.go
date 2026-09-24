package store

import (
	"context"
	"errors"
)

// Ownership outlives detachment and node deletion: a delayed agent may still
// run the old peer, so another node must never acquire its cryptographic identity.
func (s *Store) claimWireGuardIdentity(ctx context.Context, q querier, serverID, nodeID, profileID, revision int64, claim bool) error {
	secret, err := s.egressCredentials(ctx, q, serverID, profileID, revision)
	if err != nil {
		return err
	}
	public, err := secret.WireGuardPublicKey()
	if err != nil {
		return err
	}
	if claim {
		_, err = q.ExecContext(ctx, `INSERT INTO wireguard_key_owners(public_key,server_id,node_id) VALUES(?,?,?) ON CONFLICT(public_key) DO NOTHING`, public, serverID, nodeID)
		if err != nil {
			return err
		}
	}
	var owner, server int64
	err = q.QueryRowContext(ctx, `SELECT node_id,server_id FROM wireguard_key_owners WHERE public_key=?`, public).Scan(&owner, &server)
	if isNoRows(err) && !claim {
		return nil
	}
	if err != nil {
		return err
	}
	if owner != nodeID || server != serverID {
		return errors.New("该 WireGuard 私钥已属于其他节点；请为此节点配置独立 Peer 和私钥")
	}
	return nil
}

const wireguardSchema = `ALTER TABLE server_network_requirements ADD COLUMN wireguard_version INTEGER NOT NULL DEFAULT 0 CHECK(wireguard_version IN (0,1));
 CREATE TABLE wireguard_key_owners(public_key TEXT PRIMARY KEY,server_id INTEGER NOT NULL,node_id INTEGER NOT NULL);
 CREATE TRIGGER node_wireguard_requirement_insert AFTER INSERT ON node_networks
 WHEN EXISTS(SELECT 1 FROM egress_profiles WHERE id=NEW.egress_profile_id AND kind='wireguard') BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version,wireguard_version) VALUES(NEW.server_id,1,1,1)
 ON CONFLICT(server_id) DO UPDATE SET wireguard_version=1,egress_version=1;
 END;
 CREATE TRIGGER node_wireguard_requirement_update AFTER UPDATE OF egress_profile_id ON node_networks
 WHEN EXISTS(SELECT 1 FROM egress_profiles WHERE id=NEW.egress_profile_id AND kind='wireguard') BEGIN
 INSERT INTO server_network_requirements(server_id,binding_version,egress_version,wireguard_version) VALUES(NEW.server_id,1,1,1)
 ON CONFLICT(server_id) DO UPDATE SET wireguard_version=1,egress_version=1;
 END;`
