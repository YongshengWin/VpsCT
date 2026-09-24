package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
)

var ErrNetworkCoreVersion = errors.New("当前节点绑定或固定转发不支持所选内核版本，请保留已验证版本或先完成相关资源清理")

func bindingCoreVersionSupported(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		version = domain.DefaultSingBoxVersion
	}
	return agentproto.NetworkBindingSupported("singbox", version)
}

// SetSettings rejects an incompatible pin and commits the whole form atomically.
// SetNodeNetwork checks the other direction in its own transaction, so a
// concurrent new binding cannot commit on an incompatible settings snapshot.
func (s *Store) SetSettings(ctx context.Context, values map[string]string) error {
	// Choosing the default is an explicit choice of today's concrete version,
	// not permission to follow a different default in a future panel release.
	if version, ok := values[domain.SettingSingBoxVersion]; ok && strings.TrimSpace(version) == "" {
		copyValues := make(map[string]string, len(values))
		for k, v := range values {
			copyValues[k] = v
		}
		copyValues[domain.SettingSingBoxVersion] = domain.DefaultSingBoxVersion
		values = copyValues
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if version, changing := values[domain.SettingSingBoxVersion]; changing && !corecompat.SS2022Outbound(strings.TrimSpace(version)) {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM node_networks nn JOIN egress_profiles p ON p.id=nn.egress_profile_id WHERE p.kind='ss2022') +
 (SELECT count(*) FROM port_forwards f JOIN egress_profiles p ON p.id=f.egress_profile_id WHERE p.kind='ss2022' AND f.retired=0) +
 (SELECT count(*) FROM managed_transits t JOIN egress_profiles p ON p.id=t.profile_id WHERE p.kind='ss2022' AND t.stage<>'retired')`).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("%w: %s", ErrNetworkCoreVersion, corecompat.SS2022Requirement)
			}
		}
		if version, changing := values[domain.SettingSingBoxVersion]; changing && !bindingCoreVersionSupported(version) {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM node_networks nn JOIN nodes n ON n.id=nn.node_id WHERE nn.policy<>'null' AND n.core='singbox')+(SELECT count(*) FROM port_forwards)+(SELECT count(*) FROM nodes WHERE protocol='wireguard' AND source IN ('deployed','transit'))`).Scan(&count); err != nil {
				return err
			}
			// Disabled nodes/servers still retain bindings for later resume.
			if count != 0 {
				return ErrNetworkCoreVersion
			}
		}

		for _, core := range []domain.Core{domain.CoreSnell, domain.CoreMita} {
			key, fallback := bindingCoreSetting(core)
			if version, changing := values[key]; changing {
				if strings.TrimSpace(version) == "" {
					version = fallback
				}
				if !agentproto.NetworkBindingSupported(string(core), version) {
					var count int
					if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM node_networks nn JOIN nodes n ON n.id=nn.node_id WHERE nn.policy<>'null' AND n.core=?`, core).Scan(&count); err != nil {
						return err
					}
					if count != 0 {
						return ErrNetworkCoreVersion
					}
				}
			}
		}
		for _, key := range keys {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, s.seal("settings.value", values[key])); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) checkSS2022Core(ctx context.Context, q querier) error {
	var version string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, domain.SettingSingBoxVersion).Scan(s.scanSecret("settings.value", &version))
	if err != nil && !isNoRows(err) {
		return err
	}
	if !corecompat.SS2022Outbound(strings.TrimSpace(version)) {
		return fmt.Errorf("%w: %s", ErrNetworkCoreVersion, corecompat.SS2022Requirement)
	}
	return nil
}

func (s *Store) checkBindingCoreVersion(ctx context.Context, q querier, core domain.Core) error {
	key, fallback := bindingCoreSetting(core)
	if key == "" {
		return ErrNetworkCoreVersion
	}
	var version string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(s.scanSecret("settings.value", &version))
	if err != nil && !isNoRows(err) {
		return err
	}
	if strings.TrimSpace(version) == "" {
		version = fallback
	}
	if !agentproto.NetworkBindingSupported(string(core), version) {
		return ErrNetworkCoreVersion
	}
	return nil
}

func bindingCoreSetting(core domain.Core) (string, string) {
	switch core {
	case domain.CoreSingBox:
		return domain.SettingSingBoxVersion, domain.DefaultSingBoxVersion
	case domain.CoreSnell:
		return domain.SettingSnellVersion, domain.DefaultSnellVersion
	case domain.CoreMita:
		return domain.SettingMitaVersion, domain.DefaultMitaVersion
	}
	return "", ""
}
