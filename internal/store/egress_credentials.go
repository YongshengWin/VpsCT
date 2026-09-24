package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func egressCredentialLabel(serverID, profileID, revision int64) string {
	return fmt.Sprintf("egress_profile_credentials/%d/%d/%d", serverID, profileID, revision)
}

func (s *Store) egressCredentials(ctx context.Context, q querier, serverID, profileID, revision int64) (networkconfig.SOCKS5Credentials, error) {
	var out networkconfig.SOCKS5Credentials
	var encrypted string
	err := q.QueryRowContext(ctx, `SELECT credentials FROM egress_profile_credentials WHERE server_id=? AND profile_id=? AND revision=?`, serverID, profileID, revision).Scan(&encrypted)
	if isNoRows(err) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if !strings.HasPrefix(encrypted, secretPrefix) {
		return out, errors.New("出口凭据存储格式无效")
	}
	plain, err := s.decrypt(egressCredentialLabel(serverID, profileID, revision), encrypted)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return networkconfig.SOCKS5Credentials{}, errors.New("出口凭据损坏")
	}
	return out, nil
}

// Resolve a write intent without returning its secrets to an API response.
// Omitted credentials on update retain the previous immutable revision's pair.
// Switching authentication to none explicitly drops credentials in the new
// revision; old consumers keep their original pair until explicitly migrated.
func (s *Store) prepareEgressCredentials(ctx context.Context, q querier, kind string, config json.RawMessage, supplied *networkconfig.SOCKS5Credentials, previous *domain.EgressProfile) (*networkconfig.SOCKS5Credentials, error) {
	if kind == "ss2022" {
		cfg, err := networkconfig.DecodeSS2022(config)
		if err != nil {
			return nil, err
		}
		var secret networkconfig.SOCKS5Credentials
		if supplied != nil {
			secret = *supplied
		} else if previous != nil {
			secret, err = s.egressCredentials(ctx, q, previous.ServerID, previous.ID, previous.CurrentRevision)
			if err != nil {
				return nil, errors.New("请提供 SS-2022 密钥")
			}
		} else {
			return nil, errors.New("请提供 SS-2022 密钥")
		}
		if err = secret.ValidateSS2022(cfg.Method); err != nil {
			return nil, err
		}
		return &secret, nil
	}
	if kind == "wireguard" {
		cfg, err := networkconfig.DecodeWireGuard(config)
		if err != nil {
			return nil, err
		}
		var secret networkconfig.SOCKS5Credentials
		if supplied != nil {
			secret = *supplied
		} else if previous != nil {
			secret, err = s.egressCredentials(ctx, q, previous.ServerID, previous.ID, previous.CurrentRevision)
			if err != nil {
				return nil, errors.New("请提供 WireGuard 凭据")
			}
		} else {
			return nil, errors.New("请提供 WireGuard 凭据")
		}
		if err = secret.ValidateWireGuard(cfg); err != nil {
			return nil, err
		}
		return &secret, nil
	}
	if kind == "ssh" {
		cfg, err := networkconfig.DecodeSSH(config)
		if err != nil {
			return nil, err
		}
		var secret networkconfig.SOCKS5Credentials
		if supplied != nil {
			secret = *supplied
		} else if previous != nil {
			secret, err = s.egressCredentials(ctx, q, previous.ServerID, previous.ID, previous.CurrentRevision)
			if err != nil {
				return nil, errors.New("请提供 SSH 转发凭据")
			}
		} else {
			return nil, errors.New("请提供 SSH 转发凭据")
		}
		if err = secret.ValidateSSH(cfg); err != nil {
			return nil, err
		}
		return &secret, nil
	}
	if kind != "socks5" {
		if supplied != nil {
			return nil, errors.New("此出口类型不能附带凭据")
		}
		return nil, nil
	}
	cfg, err := networkconfig.DecodeSOCKS5(config)
	if err != nil {
		return nil, err
	}
	if cfg.Authentication == "none" {
		if supplied != nil {
			if err := supplied.Validate("none"); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	var secret networkconfig.SOCKS5Credentials
	if supplied != nil {
		secret = *supplied
	} else if previous != nil {
		secret, err = s.egressCredentials(ctx, q, previous.ServerID, previous.ID, previous.CurrentRevision)
		if errors.Is(err, ErrNotFound) {
			return nil, errors.New("请提供 SOCKS5 上游用户名和密码")
		}
		if err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("请提供 SOCKS5 上游用户名和密码")
	}
	if err = secret.Validate(cfg.Authentication); err != nil {
		return nil, err
	}
	return &secret, nil
}

func (s *Store) saveEgressCredentials(ctx context.Context, q querier, serverID, profileID, revision int64, secret *networkconfig.SOCKS5Credentials) error {
	if secret == nil {
		return nil
	}
	b, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO egress_profile_credentials(server_id,profile_id,revision,credentials) VALUES(?,?,?,?)`, serverID, profileID, revision, s.seal(egressCredentialLabel(serverID, profileID, revision), string(b)))
	return err
}
