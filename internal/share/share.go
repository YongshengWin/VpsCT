// Package share manages customer allotments: dedicated inbounds per VPS,
// metered quotas with a small state machine, and the linked subscription.
package share

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ctlvps/internal/auth"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

// Manager coordinates shares across store, provisioning and desired state.
type Manager struct {
	Store   *store.Store
	Desired *desired.Builder
	Now     func() time.Time
	// OnEvent is called for notable transitions (Telegram etc.).
	OnEvent func(ctx context.Context, sh domain.Share, kind, detail string)
}

// New builds a Manager.
func New(st *store.Store, d *desired.Builder) *Manager {
	return &Manager{Store: st, Desired: d, Now: func() time.Time { return time.Now().UTC() }}
}

func (m *Manager) event(ctx context.Context, sh domain.Share, kind, detail string) {
	_ = m.Store.AddShareEvent(ctx, sh.ID, kind, detail)
	if m.OnEvent != nil {
		m.OnEvent(ctx, sh, kind, detail)
	}
}

// Create persists a share, provisions its nodes and subscription.
func (m *Manager) Create(ctx context.Context, sh *domain.Share) (string, error) {
	if strings.TrimSpace(sh.Name) == "" {
		return "", errors.New("名称不能为空")
	}
	if sh.ResetDay < 0 || sh.ResetDay > 31 {
		return "", errors.New("重置日必须在 1-28 之间（0 表示不重置，29–31 表示每月最后一天）")
	}
	sh.ResetDay = domain.NormalizeResetDay(sh.ResetDay)
	now := m.Now()
	sh.Status = domain.ShareActive
	if sh.ResetDay > 0 {
		sh.PeriodStart = traffic.PeriodStart(now, sh.ResetDay)
	} else {
		sh.PeriodStart = now
	}
	if err := m.Store.CreateShare(ctx, sh); err != nil {
		return "", err
	}
	token := auth.NewSubscriptionToken()
	sub := &domain.Subscription{
		Name:           sh.Name,
		Kind:           domain.SubShare,
		Token:          token,
		TokenHash:      auth.HashToken(token),
		TokenHint:      auth.TokenHint(token),
		TemplateID:     sh.TemplateID,
		DefaultFormat:  "mihomo",
		UserinfoHeader: true,
		ShowInfoNodes:  true,
		ShareID:        &sh.ID,
		Enabled:        true,
	}
	if sh.UserID != nil {
		sub.OwnerUserID = *sh.UserID
		sub.AllowedUserIDs = []int64{*sh.UserID}
	}
	if m.Store.GetSettingBool(ctx, domain.SettingShortLinks, true) {
		sub.ShortCode = auth.NewSubscriptionToken()
	}
	if err := m.Store.CreateSubscription(ctx, sub); err != nil {
		return "", err
	}
	sh.SubscriptionID = &sub.ID
	if err := m.Store.UpdateShare(ctx, sh); err != nil {
		return "", err
	}
	if err := m.EnsureNodes(ctx, sh); err != nil {
		return token, err
	}
	m.event(ctx, *sh, "created", fmt.Sprintf("quota=%d reset_day=%d", sh.QuotaBytes, sh.ResetDay))
	return token, nil
}

// Update saves edits and reconciles nodes.
func (m *Manager) Update(ctx context.Context, sh *domain.Share) error {
	prev, err := m.Store.GetShare(ctx, sh.ID)
	if err != nil {
		return err
	}
	// keep runtime fields
	sh.UsedUpload, sh.UsedDownload, sh.PeriodStart = prev.UsedUpload, prev.UsedDownload, prev.PeriodStart
	sh.SubscriptionID = prev.SubscriptionID
	if sh.Status == "" {
		sh.Status = prev.Status
	}
	if err := m.Store.UpdateShare(ctx, sh); err != nil {
		return err
	}
	if sub, err := m.Store.GetSubscriptionByShare(ctx, sh.ID); err == nil {
		sub.Name = sh.Name
		sub.TemplateID = sh.TemplateID
		// Share UI only picks a template; leftover stock groups/rules would
		// hide ⚡️ smart / 地区组 / RULE-SET from the profile.
		sub.ProxyGroups = nil
		sub.Rules = nil
		if sh.UserID != nil {
			sub.OwnerUserID = *sh.UserID
			sub.AllowedUserIDs = []int64{*sh.UserID}
		} else {
			sub.AllowedUserIDs = []int64{}
		}
		_ = m.Store.UpdateSubscription(ctx, &sub)
	}
	if err := m.EnsureNodes(ctx, sh); err != nil {
		return err
	}
	// quota may have been raised: re-evaluate
	return m.evaluate(ctx, sh.ID)
}

// EnsureNodes creates missing dedicated nodes for every (server, protocol)
// target and revokes nodes whose target was removed, then republishes the
// affected servers.
func (m *Manager) EnsureNodes(ctx context.Context, sh *domain.Share) error {
	existing, err := m.Store.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID, IncludeRevoked: true})
	if err != nil {
		return err
	}
	type key struct {
		server int64
		proto  string
	}
	have := map[key]domain.Node{}
	for _, n := range existing {
		if n.ServerID != nil {
			have[key{*n.ServerID, n.Protocol}] = n
		}
	}
	want := map[key]bool{}
	affected := map[int64]bool{}
	for _, t := range sh.Targets {
		server, err := m.Store.GetServer(ctx, t.ServerID)
		if err != nil {
			continue
		}
		for _, proto := range t.Protocols {
			k := key{t.ServerID, proto}
			want[k] = true
			if n, ok := have[k]; ok {
				if n.Revoked {
					n.Revoked = false
					n.Enabled = true
					_ = provision.RegenerateCredentials(&n, server)
					if err := m.Store.UpdateNode(ctx, &n); err != nil {
						return err
					}
					affected[t.ServerID] = true
				}
				continue
			}
			used, err := m.Store.UsedListenPorts(ctx, t.ServerID)
			if err != nil {
				return err
			}
			port, err := provision.AllocatePort(used)
			if err != nil {
				return err
			}
			node, err := provision.NewNode(server, "", provision.Options{Name: fmt.Sprintf("%s · %s · %s", sh.Name, server.Name, strings.ToUpper(proto)), Protocol: proto, Port: port})
			if err != nil {
				return err
			}
			sid := sh.ID
			node.ShareID = &sid
			if sh.UserID != nil {
				node.OwnerUserID = *sh.UserID
			}
			if err := m.Store.CreateNode(ctx, &node); err != nil {
				return err
			}
			affected[t.ServerID] = true
		}
	}
	for k, n := range have {
		if !want[k] && !n.Revoked {
			n.Revoked = true
			n.Enabled = false
			if err := m.Store.UpdateNode(ctx, &n); err != nil {
				return err
			}
			affected[k.server] = true
		}
	}
	for sid := range affected {
		if _, _, err := m.Desired.Publish(ctx, sid); err != nil {
			return err
		}
	}
	return nil
}

// SetConnlogEnabled turns connection logging on or off for a share and republishes.
func (m *Manager) SetConnlogEnabled(ctx context.Context, id int64, enabled bool) error {
	sh, err := m.Store.GetShare(ctx, id)
	if err != nil {
		return err
	}
	if sh.ConnlogEnabled == enabled {
		return nil
	}
	sh.ConnlogEnabled = enabled
	if err := m.Store.UpdateShare(ctx, &sh); err != nil {
		return err
	}
	return m.republishShareServers(ctx, id)
}

func (m *Manager) republishShareServers(ctx context.Context, shareID int64) error {
	nodes, err := m.Store.ListNodes(ctx, store.NodeFilter{ShareID: &shareID, IncludeRevoked: true})
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, n := range nodes {
		if n.ServerID != nil && !seen[*n.ServerID] {
			seen[*n.ServerID] = true
			if _, _, err := m.Desired.Publish(ctx, *n.ServerID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ApplyDeltas adds metered traffic to shares and enforces quotas.
func (m *Manager) ApplyDeltas(ctx context.Context, deltas []traffic.ShareDelta) error {
	for _, d := range deltas {
		if _, _, err := m.Store.AddShareUsage(ctx, d.ShareID, d.Up, d.Down); err != nil {
			return err
		}
		if err := m.evaluate(ctx, d.ShareID); err != nil {
			return err
		}
	}
	return nil
}

// evaluate applies the state machine to one share.
func (m *Manager) evaluate(ctx context.Context, id int64) error {
	sh, err := m.Store.GetShare(ctx, id)
	if err != nil {
		return err
	}
	now := m.Now()
	next := sh.Status
	switch sh.Status {
	case domain.ShareRevoked, domain.SharePaused:
		return nil // manual states are sticky
	}
	// period rollover
	if sh.ResetDay > 0 {
		ps := traffic.PeriodStart(now, sh.ResetDay)
		if ps.After(sh.PeriodStart) {
			if err := m.Store.ResetShareUsage(ctx, sh.ID, ps); err != nil {
				return err
			}
			sh.UsedUpload, sh.UsedDownload, sh.PeriodStart = 0, 0, ps
			m.event(ctx, sh, "period_reset", ps.Format("2006-01-02"))
			if sh.Status == domain.ShareExhausted {
				next = domain.ShareActive
			}
		}
	}
	if sh.ExpiresAt != nil && now.After(*sh.ExpiresAt) {
		next = domain.ShareExpired
	} else if sh.Status == domain.ShareExpired {
		next = domain.ShareActive
	}
	if next != domain.ShareExpired && sh.QuotaBytes > 0 {
		if domain.Total(sh.UsedUpload, sh.UsedDownload) >= sh.QuotaBytes {
			next = domain.ShareExhausted
		} else if next == domain.ShareExhausted {
			next = domain.ShareActive
		}
	}
	if next != sh.Status {
		if err := m.Store.SetShareStatus(ctx, sh.ID, next); err != nil {
			return err
		}
		sh.Status = next
		m.event(ctx, sh, "status", string(next))
		return m.republishShareServers(ctx, sh.ID)
	}
	return nil
}

// Tick re-evaluates all shares (period resets, expiry).
func (m *Manager) Tick(ctx context.Context) error {
	shares, err := m.Store.ListShares(ctx, nil)
	if err != nil {
		return err
	}
	for _, sh := range shares {
		if err := m.evaluate(ctx, sh.ID); err != nil {
			return err
		}
	}
	return nil
}

// Pause blocks a share manually.
func (m *Manager) Pause(ctx context.Context, id int64) error {
	sh, err := m.Store.GetShare(ctx, id)
	if err != nil {
		return err
	}
	if sh.Status == domain.ShareRevoked {
		return errors.New("已撤销的分享不能暂停")
	}
	if err := m.Store.SetShareStatus(ctx, id, domain.SharePaused); err != nil {
		return err
	}
	sh.Status = domain.SharePaused
	m.event(ctx, sh, "paused", "")
	return m.republishShareServers(ctx, id)
}

// Resume re-activates a paused share (quota/expiry re-evaluated).
func (m *Manager) Resume(ctx context.Context, id int64) error {
	sh, err := m.Store.GetShare(ctx, id)
	if err != nil {
		return err
	}
	if sh.Status == domain.ShareRevoked {
		return errors.New("已撤销的分享需要重新签发")
	}
	if err := m.Store.SetShareStatus(ctx, id, domain.ShareActive); err != nil {
		return err
	}
	sh.Status = domain.ShareActive
	m.event(ctx, sh, "resumed", "")
	if err := m.republishShareServers(ctx, id); err != nil {
		return err
	}
	return m.evaluate(ctx, id)
}

// ResetUsage zeroes the current period manually.
func (m *Manager) ResetUsage(ctx context.Context, id int64) error {
	sh, err := m.Store.GetShare(ctx, id)
	if err != nil {
		return err
	}
	if err := m.Store.ResetShareUsage(ctx, id, m.Now()); err != nil {
		return err
	}
	m.event(ctx, sh, "manual_reset", "")
	return m.evaluate(ctx, id)
}

// Revoke tears down every node of the share and disables its subscription.
func (m *Manager) Revoke(ctx context.Context, id int64) error {
	sh, err := m.Store.GetShare(ctx, id)
	if err != nil {
		return err
	}
	nodes, err := m.Store.ListNodes(ctx, store.NodeFilter{ShareID: &id, IncludeRevoked: true})
	if err != nil {
		return err
	}
	for _, n := range nodes {
		n.Revoked = true
		n.Enabled = false
		if err := m.Store.UpdateNode(ctx, &n); err != nil {
			return err
		}
	}
	if err := m.Store.SetShareStatus(ctx, id, domain.ShareRevoked); err != nil {
		return err
	}
	if sub, err := m.Store.GetSubscriptionByShare(ctx, id); err == nil {
		sub.Enabled = false
		_ = m.Store.UpdateSubscription(ctx, &sub)
	}
	sh.Status = domain.ShareRevoked
	m.event(ctx, sh, "revoked", "")
	return m.republishShareServers(ctx, id)
}

// Reissue rotates all credentials and the subscription token of a share and
// re-activates it. Returns the new subscription token.
func (m *Manager) Reissue(ctx context.Context, id int64) (string, error) {
	sh, err := m.Store.GetShare(ctx, id)
	if err != nil {
		return "", err
	}
	nodes, err := m.Store.ListNodes(ctx, store.NodeFilter{ShareID: &id, IncludeRevoked: true})
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.ServerID == nil {
			continue
		}
		server, err := m.Store.GetServer(ctx, *n.ServerID)
		if err != nil {
			continue
		}
		if err := provision.RegenerateCredentials(&n, server); err != nil {
			return "", err
		}
		n.Revoked = false
		n.Enabled = true
		if err := m.Store.UpdateNode(ctx, &n); err != nil {
			return "", err
		}
	}
	token := auth.NewSubscriptionToken()
	if sub, err := m.Store.GetSubscriptionByShare(ctx, id); err == nil {
		sub.Token = token
		sub.TokenHash = auth.HashToken(token)
		sub.TokenHint = auth.TokenHint(token)
		sub.Enabled = true
		if sub.ShortCode != "" {
			sub.ShortCode = auth.NewSubscriptionToken()
		}
		if err := m.Store.UpdateSubscription(ctx, &sub); err != nil {
			return "", err
		}
	}
	if err := m.Store.SetShareStatus(ctx, id, domain.ShareActive); err != nil {
		return "", err
	}
	sh.Status = domain.ShareActive
	m.event(ctx, sh, "reissued", "")
	if err := m.EnsureNodes(ctx, &sh); err != nil {
		return token, err
	}
	if err := m.republishShareServers(ctx, id); err != nil {
		return token, err
	}
	return token, m.evaluate(ctx, id)
}

// Delete removes a share, its nodes and subscription.
func (m *Manager) Delete(ctx context.Context, id int64) error {
	nodes, err := m.Store.ListNodes(ctx, store.NodeFilter{ShareID: &id, IncludeRevoked: true})
	if err != nil {
		return err
	}
	servers := map[int64]bool{}
	for _, n := range nodes {
		if n.ServerID != nil {
			servers[*n.ServerID] = true
		}
		if err := m.Store.DeleteNode(ctx, n.ID); err != nil {
			return err
		}
	}
	if err := m.Store.DeleteShare(ctx, id); err != nil {
		return err
	}
	for sid := range servers {
		if _, _, err := m.Desired.Publish(ctx, sid); err != nil {
			return err
		}
	}
	return nil
}

// Usage summarises the current period for the API.
type Usage struct {
	Used      int64      `json:"used"`
	Inbound   int64      `json:"inbound"`
	Outbound  int64      `json:"outbound"`
	Total     int64      `json:"total"`
	OneWay    int64      `json:"one_way"`
	TwoWay    int64      `json:"two_way"`
	Quota     int64      `json:"quota"`
	Percent   float64    `json:"percent"`
	NextReset *time.Time `json:"next_reset,omitempty"`
	Remaining int64      `json:"remaining"`
}

// UsageOf computes display figures for a share.
func (m *Manager) UsageOf(sh domain.Share) Usage {
	in, out := sh.UsedUpload, sh.UsedDownload
	total := domain.Total(in, out)
	u := Usage{
		Used:      total,
		Inbound:   in,
		Outbound:  out,
		Total:     total,
		OneWay:    out,
		TwoWay:    total,
		Quota:     sh.QuotaBytes,
		Remaining: -1,
	}
	if sh.QuotaBytes > 0 {
		u.Percent = float64(u.Used) / float64(sh.QuotaBytes) * 100
		u.Remaining = sh.QuotaBytes - u.Used
		if u.Remaining < 0 {
			u.Remaining = 0
		}
	}
	if sh.ResetDay > 0 {
		nr := traffic.NextReset(m.Now(), sh.ResetDay)
		u.NextReset = &nr
	}
	return u
}

// EvaluateDeltas enforces quotas for usage already committed atomically by ingestion.
func (m *Manager) EvaluateDeltas(ctx context.Context, deltas []traffic.ShareDelta) error {
	for _, d := range deltas {
		if d.PeriodReset {
			if err := m.republishShareServers(ctx, d.ShareID); err != nil {
				return err
			}
		}
		if err := m.evaluate(ctx, d.ShareID); err != nil {
			return err
		}
	}
	return nil
}
