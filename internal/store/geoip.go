package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"ctlvps/internal/geoip"
)

// RefreshGeoIPPolicy replaces only the materialized address data of an
// existing country-deny policy. It preserves the selected countries and
// increments the node network revision so desired publication cannot miss the
// change. The caller owns the provider download and supplies its verified
// version and ranges.
func (s *Store) RefreshGeoIPPolicy(ctx context.Context, nodeID int64, ranges geoip.CountryRanges) (int64, bool, error) {
	var serverID int64
	changed := false
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		n, err := s.scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=?`, nodeID))
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if n.Network == nil || n.ServerID == nil || n.Network.Ingress == nil || n.Network.Ingress.Mode != "deny_countries" {
			return nil
		}
		if n.Network.Ingress.GeoProvider == ranges.Provider && n.Network.Ingress.GeoVersion == ranges.Version && sameStrings(n.Network.Ingress.GeoIPv4CIDRs, ranges.IPv4) && sameStrings(n.Network.Ingress.GeoIPv6CIDRs, ranges.IPv6) {
			serverID = *n.ServerID
			return nil
		}
		policy := *n.Network
		ingress := *n.Network.Ingress
		ingress.GeoProvider, ingress.GeoVersion = ranges.Provider, ranges.Version
		ingress.GeoIPv4CIDRs = append([]string(nil), ranges.IPv4...)
		ingress.GeoIPv6CIDRs = append([]string(nil), ranges.IPv6...)
		policy.Ingress = &ingress
		if err := policy.Validate(); err != nil {
			return err
		}
		payload, err := json.Marshal(&policy)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE node_networks SET revision=revision+1,policy=?,updated_at=? WHERE node_id=? AND revision=?`, string(payload), fmtTime(s.Now()), nodeID, n.NetworkRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("GeoIP 策略版本已变化，请重新刷新")
		}
		serverID, changed = *n.ServerID, true
		return nil
	})
	return serverID, changed, err
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
