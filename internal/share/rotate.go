package share

import (
	"ctlvps/internal/domain"
	"ctlvps/internal/provision"
	"encoding/json"
	"errors"
)

// Regeneration supplies independent fresh key material; the running entry is
// the authority for all non-secret settings (including WG routes and MTU).
func rotateCredentials(n *domain.Node, server domain.Server) error {
	fresh := *n
	if err := provision.RegenerateCredentials(&fresh, server); err != nil {
		return err
	}
	if fresh.Core != n.Core {
		return errors.New("重新签发不能更改入口内核")
	}
	var client, svc []string
	switch n.Protocol {
	case domain.ProtocolVLESS:
		client = []string{"uuid", "reality-opts"}
		svc = []string{"uuid", "reality_private_key", "reality_public_key", "reality_short_id"}
	case domain.ProtocolWireGuard:
		client = []string{"private-key", "public-key", "pre-shared-key"}
		svc = []string{"private_key", "peer_public_key", "pre_shared_key"}
	case domain.ProtocolSnell:
		client = []string{"psk"}
		svc = client
	case domain.ProtocolTUIC:
		client = []string{"uuid", "password"}
		svc = client
	case domain.ProtocolHysteria2:
		client = []string{"password", "obfs-password"}
		svc = []string{"password", "obfs_password"}
	case domain.ProtocolShadowsocks, domain.ProtocolTrojan, domain.ProtocolAnyTLS:
		client = []string{"password"}
		svc = client
	case domain.ProtocolMieru:
		client = []string{"username", "password"}
		svc = client
	default:
		return errors.New("无法识别历史入口凭据")
	}
	merge := func(old, next json.RawMessage, keys []string) (json.RawMessage, error) {
		var a, b map[string]json.RawMessage
		if json.Unmarshal(old, &a) != nil || json.Unmarshal(next, &b) != nil || a == nil || b == nil {
			return nil, errors.New("历史入口参数无效")
		}
		for _, key := range keys {
			v, ok := b[key]
			_, existed := a[key]
			if !ok && !existed {
				continue
			}
			if !ok || !existed {
				return nil, errors.New("历史入口凭据不完整，不能安全重签")
			}
			a[key] = v
		}
		return json.Marshal(a)
	}
	var err error
	n.Params, err = merge(n.Params, fresh.Params, client)
	if err != nil {
		return err
	}
	n.ServerParams, err = merge(n.ServerParams, fresh.ServerParams, svc)
	return err
}
