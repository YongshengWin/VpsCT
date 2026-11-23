package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/nodecombo"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
)

var ErrEntryConfig = errors.New("分享组合配置无效")

// Legacy protocol-only selections keep the provisioning defaults of the old API.
// New selections persist explicit defaults before any allocation. Changes to
// future defaults must not change an existing entry's meaning during recovery.
func normalizeEntries(targets []domain.ShareTarget) error {
	for _, t := range targets {
		if err := t.ValidateEntries(); err != nil {
			return fmt.Errorf("%w：%v", ErrEntryConfig, err)
		}
		for i := range t.Entries {
			c := &t.Entries[i].Snapshot
			switch c.Protocol {
			case domain.ProtocolShadowsocks:
				if c.SSNetwork == "" {
					c.SSNetwork = "tcp"
				}
				if c.SSMethod == "" {
					c.SSMethod = "2022-blake3-aes-128-gcm"
				}
			case domain.ProtocolMieru:
				if c.MieruTransport == "" {
					c.MieruTransport = "TCP"
				}
			case domain.ProtocolSnell:
				if c.SnellVersion == 0 {
					c.SnellVersion = 4
				} else if c.SnellVersion != 4 && c.SnellVersion != 5 {
					return fmt.Errorf("%w：Snell 快照版本只支持 4 或 5", ErrEntryConfig)
				}
			case domain.ProtocolTrojan:
				// Old explicit Trojan entries stored only protocol. Preserve that
				// snapshot so recovery continues to match their immutable bindings.
				if c.Trojan != nil {
					nv, err := c.Trojan.Normalized()
					if err != nil {
						return fmt.Errorf("%w：%v", ErrEntryConfig, err)
					}
					c.Trojan = &nv
				}
			case domain.ProtocolVLESS:
				v := nodecombo.VLESS{}
				if c.VLESS != nil {
					v = *c.VLESS
				}
				nv, err := v.Normalized()
				if err != nil {
					return fmt.Errorf("%w：%v", ErrEntryConfig, err)
				}
				c.VLESS = &nv
			}
		}
	}
	return nil
}

type entryKey struct {
	server int64
	id     string
	legacy bool
}
type entrySelection struct {
	config domain.ShareEntryConfig
	entry  *domain.ShareEntry
}

func selections(t domain.ShareTarget) []entrySelection {
	out := make([]entrySelection, 0, len(t.Protocols)+len(t.Entries))
	for _, p := range t.Protocols {
		out = append(out, entrySelection{config: domain.ShareEntryConfig{Protocol: p}})
	}
	for i := range t.Entries {
		out = append(out, entrySelection{t.Entries[i].Snapshot, &t.Entries[i]})
	}
	return out
}

// Check the deployed combination, not just the binding ID, before granting
// access again. Consistently edited client/server parameters may still have
// drifted from the original snapshot; that is not a credential rotation.
func matchesSnapshot(n domain.Node, c domain.ShareEntryConfig) bool {
	var p map[string]any
	if json.Unmarshal(n.ServerParams, &p) != nil || p == nil || n.Protocol != c.Protocol {
		return false
	}
	if c.ManagedTLS() && (p["tls_domain"] != c.Domain || p["cert_mode"] != c.CertMode || c.CertID != "" && p["cert_id"] != c.CertID || c.CertID == "" && p["cert_id"] != nil) {
		return false
	}
	switch c.Protocol {
	case domain.ProtocolVLESS:
		v, err := provision.VLESSFromNode(n)
		return err == nil && reflect.DeepEqual(v, c.VLESS)
	case domain.ProtocolTrojan:
		v, err := provision.TrojanFromNode(n)
		if err != nil {
			return false
		}
		if c.Trojan == nil {
			return v.Transport == "tcp" && v.Security == "tls"
		}
		return reflect.DeepEqual(v, c.Trojan)
	case domain.ProtocolShadowsocks:
		network, _ := p["network"].(string)
		if network == "" {
			network = "both"
		}
		return p["method"] == c.SSMethod && network == c.SSNetwork
	case domain.ProtocolSnell:
		version, present := p["version"]
		if !present {
			return c.SnellVersion == 4
		}
		return fmt.Sprint(version) == fmt.Sprint(c.SnellVersion)
	case domain.ProtocolMieru:
		return p["transport"] == c.MieruTransport
	case domain.ProtocolHysteria2:
		_, obfs := p["obfs_password"].(string)
		return obfs == c.Obfs
	}
	return true
}

func (m *Manager) reconcile(ctx context.Context, prev domain.Share, sh *domain.Share, saveSettings bool, rotateToken string) error {
	if err := normalizeEntries(sh.Targets); err != nil {
		return err
	}
	existing, err := m.Store.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID, IncludeRevoked: true})
	if err != nil {
		return err
	}
	bindings, err := m.Store.ShareEntryBindings(ctx, sh.ID)
	if err != nil {
		return err
	}
	have := map[entryKey]domain.Node{}
	for _, n := range existing {
		if n.ServerID == nil {
			return errors.New("专属入口缺少服务器，无法安全恢复")
		}
		k := entryKey{*n.ServerID, n.Protocol, true}
		if b, ok := bindings[n.ID]; ok {
			k.id, k.legacy = b.ID, false
		}
		if _, ok := have[k]; ok {
			return errors.New("同一分享目标存在多个历史入口，无法安全匹配")
		}
		have[k] = n
	}
	creates, updates := []domain.Node{}, []domain.Node{}
	newEntries := map[int]*domain.ShareEntry{}
	reserved := map[int64]map[int]bool{}
	for _, t := range sh.Targets {
		server, err := m.Store.GetServer(ctx, t.ServerID)
		if err != nil {
			return err
		}
		for _, sel := range selections(t) {
			c, e := sel.config, sel.entry
			proto := c.Protocol
			k := entryKey{t.ServerID, proto, true}
			label := strings.ToUpper(proto)
			if e != nil {
				k.id, k.legacy = e.ID, false
				label += " · " + e.ID
			}
			if n, ok := have[k]; ok {
				delete(have, k)
				if e != nil {
					bound := bindings[n.ID].Snapshot
					if c.ManagedTLS() {
						// Replaying an omitted default means the original choice, not
						// today's server address or certificate policy.
						if c.Domain == "" {
							c.Domain = bound.Domain
						}
						if c.CertMode == "" {
							c.CertMode = bound.CertMode
						}
						e.Snapshot = c
					}
					if !reflect.DeepEqual(bound, c) || !matchesSnapshot(n, c) {
						return store.ErrShareSnapshot
					}
					if e.NodeID != 0 && e.NodeID != n.ID {
						return ErrEntryConfig
					}
					e.NodeID = n.ID
				}
				if rotateToken != "" || n.Revoked && prev.Status != domain.ShareRevoked {
					if err := rotateCredentials(&n, server); err != nil {
						return fmt.Errorf("无法安全恢复节点 %d：%w", n.ID, err)
					}
					n.Revoked = false
					n.Enabled = true
					updates = append(updates, n)
				}
				continue
			}
			if e != nil && e.NodeID != 0 {
				return ErrEntryConfig
			}
			if rotateToken != "" {
				return errors.New("专属入口记录缺失，不能在重签时替换线路")
			}
			if prev.Status == domain.ShareRevoked {
				return errors.New("已撤销的分享不能添加入口，请先重签")
			}
			used := reserved[t.ServerID]
			if used == nil {
				used, err = m.Store.UsedListenPorts(ctx, t.ServerID)
				if err != nil {
					return err
				}
				reserved[t.ServerID] = used
			}
			nat, err := m.Store.NATPortConfig(ctx, t.ServerID)
			if err != nil {
				return err
			}
			port, err := provision.AllocateNodePort(used, nat, proto, map[string]any{"transport": c.MieruTransport, "network": c.SSNetwork})
			if err != nil {
				return err
			}
			n, err := provision.NewNode(server, "", provision.Options{Name: fmt.Sprintf("%s · %s · %s", sh.Name, server.Name, label), Protocol: proto, Port: port, Network: t.Network, VLESS: c.VLESS, Trojan: c.Trojan, Domain: c.Domain, CertMode: c.CertMode, CertID: c.CertID, SSMethod: c.SSMethod, SSNetwork: c.SSNetwork, SnellVersion: c.SnellVersion, MieruTransport: c.MieruTransport, Obfs: c.Obfs})
			if err != nil {
				return fmt.Errorf("%w：%v", ErrEntryConfig, err)
			}
			n.ShareID = &sh.ID
			if sh.UserID != nil {
				n.OwnerUserID = *sh.UserID
			}
			if e != nil {
				if c.ManagedTLS() {
					// Freeze the effective defaults selected by provisioning. Changing
					// the server later must not alter or invalidate this entry.
					var tls struct {
						Domain string `json:"tls_domain"`
						Mode   string `json:"cert_mode"`
					}
					if err := json.Unmarshal(n.ServerParams, &tls); err != nil {
						return err
					}
					e.Snapshot.Domain, e.Snapshot.CertMode = tls.Domain, tls.Mode
				}
				newEntries[len(creates)] = e
			}
			creates = append(creates, n)
			used[port] = true
		}
	}
	for _, n := range have {
		if !n.Revoked {
			n.Revoked = true
			n.Enabled = false
			updates = append(updates, n)
		}
	}
	return m.Store.CommitShareEntries(ctx, prev, sh, saveSettings, creates, updates, newEntries, rotateToken)
}
