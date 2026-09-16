package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

// Service builds and renders subscriptions on top of the store.
type Service struct {
	Store   *store.Store
	Fetcher *Fetcher
	Now     func() time.Time
}

// NewService constructs a Service.
func NewService(st *store.Store) *Service {
	return &Service{Store: st, Fetcher: NewFetcher(), Now: func() time.Time { return time.Now().UTC() }}
}

// Seed inserts the builtin templates and presets when none exist yet.
func (s *Service) Seed(ctx context.Context) error {
	tpls, err := s.Store.ListTemplates(ctx)
	if err != nil {
		return err
	}
	existing := make(map[string]domain.RuleTemplate, len(tpls))
	for _, t := range tpls {
		existing[t.Name] = t
	}
	for _, want := range BuiltinTemplates() {
		want := want
		if cur, ok := existing[want.Name]; ok {
			if cur.IsBuiltin && (cur.Content != want.Content || cur.Description != want.Description || cur.Kind != want.Kind) {
				cur.Content = want.Content
				cur.Description = want.Description
				cur.Kind = want.Kind
				if err := s.Store.UpdateTemplate(ctx, &cur); err != nil {
					return err
				}
			}
			continue
		}
		if err := s.Store.CreateTemplate(ctx, &want); err != nil {
			return err
		}
	}
	presets, err := s.Store.ListPresets(ctx)
	if err != nil {
		return err
	}
	existingP := make(map[string]domain.ProxyGroupPreset, len(presets))
	for _, p := range presets {
		existingP[p.Name] = p
	}
	for _, want := range BuiltinPresets() {
		want := want
		if cur, ok := existingP[want.Name]; ok {
			if cur.IsBuiltin && !presetEqual(cur, want) {
				cur.Groups = want.Groups
				cur.Rules = want.Rules
				if err := s.Store.UpdatePreset(ctx, &cur); err != nil {
					return err
				}
			}
			continue
		}
		if err := s.Store.CreatePreset(ctx, &want); err != nil {
			return err
		}
	}
	return nil
}

func presetEqual(a, b domain.ProxyGroupPreset) bool {
	ga, _ := json.Marshal(a.Groups)
	gb, _ := json.Marshal(b.Groups)
	ra, _ := json.Marshal(a.Rules)
	rb, _ := json.Marshal(b.Rules)
	return string(ga) == string(gb) && string(ra) == string(rb)
}

// ErrDisabled is returned for disabled / expired subscriptions.
var ErrDisabled = errors.New("subscription disabled")

// serverHosts maps server id -> best public host for deployed nodes.
func (s *Service) serverHosts(ctx context.Context) map[int64]string {
	out := map[int64]string{}
	servers, err := s.Store.ListServers(ctx)
	if err != nil {
		return out
	}
	agents, _ := s.Store.ListAgents(ctx)
	byServer := map[int64]domain.Agent{}
	for _, a := range agents {
		byServer[a.ServerID] = a
	}
	for _, sv := range servers {
		host := sv.PublicHost
		if host == "" {
			if a, ok := byServer[sv.ID]; ok {
				if a.PublicIPv4 != "" {
					host = a.PublicIPv4
				} else {
					host = a.PublicIPv6
				}
			}
		}
		out[sv.ID] = host
	}
	return out
}

// ProxyFor converts a node into a client proxy, filling the server address of
// deployed nodes from the VPS record.
func ProxyFor(n domain.Node, hosts map[int64]string) proxynode.Proxy {
	p := proxynode.FromDomain(n)
	if p.Server == "" && n.ServerID != nil {
		p.Server = hosts[*n.ServerID]
	}
	if p.Port == 0 && n.ListenPort > 0 {
		p.Port = n.ListenPort
	}
	return p
}

// SelectNodes resolves a NodeSelection into nodes (enabled, not revoked).
func (s *Service) SelectNodes(ctx context.Context, sel domain.NodeSelection) ([]domain.Node, error) {
	seen := map[int64]bool{}
	var out []domain.Node
	add := func(n domain.Node) {
		if seen[n.ID] || !n.Enabled || n.Revoked {
			return
		}
		seen[n.ID] = true
		out = append(out, n)
	}
	if len(sel.NodeIDs) > 0 {
		nodes, err := s.Store.ListNodes(ctx, store.NodeFilter{IDs: sel.NodeIDs})
		if err != nil {
			return nil, err
		}
		byID := map[int64]domain.Node{}
		for _, n := range nodes {
			byID[n.ID] = n
		}
		for _, id := range sel.NodeIDs { // preserve the user's order
			if n, ok := byID[id]; ok {
				add(n)
			}
		}
	}
	for _, extID := range sel.ExternalSubIDs {
		id := extID
		nodes, err := s.Store.ListNodes(ctx, store.NodeFilter{ExternalSubID: &id})
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			add(n)
		}
	}
	if sel.IncludeAll {
		nodes, err := s.Store.ListNodes(ctx, store.NodeFilter{})
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if n.ShareID == nil { // dedicated share inbounds never leak into general subscriptions
				add(n)
			}
		}
	}
	if len(sel.Tags) > 0 {
		nodes, err := s.Store.ListNodes(ctx, store.NodeFilter{})
		if err != nil {
			return nil, err
		}
		want := map[string]bool{}
		for _, t := range sel.Tags {
			want[t] = true
		}
		for _, n := range nodes {
			for _, t := range n.Tags {
				if want[t] {
					add(n)
					break
				}
			}
		}
	}
	if sel.Filter != "" {
		if re, err := regexp.Compile(sel.Filter); err == nil {
			filtered := out[:0]
			for _, n := range out {
				if re.MatchString(n.Name) {
					filtered = append(filtered, n)
				}
			}
			out = filtered
		}
	}
	if sel.ExcludeFilter != "" {
		if re, err := regexp.Compile(sel.ExcludeFilter); err == nil {
			filtered := out[:0]
			for _, n := range out {
				if !re.MatchString(n.Name) {
					filtered = append(filtered, n)
				}
			}
			out = filtered
		}
	}
	return out, nil
}

// subscriptionFollowsTemplate is true when the profile's groups/rules must
// come from the selected (or builtin) template, not leftover editor fields.
func subscriptionFollowsTemplate(sub domain.Subscription) bool {
	return sub.Kind == domain.SubShare
}

// Build produces the format-independent bundle for a subscription.
func (s *Service) Build(ctx context.Context, sub domain.Subscription) (*Bundle, error) {
	if !sub.Kind.Supported() {
		return nil, errors.New("此订阅类型已停用，请从节点库生成订阅并选择模板")
	}
	now := s.Now()
	b := &Bundle{Name: sub.Name, GeneratedAt: now}
	if !subscriptionFollowsTemplate(sub) {
		b.Rules = sub.Rules
	}
	if len(sub.RuleProviders) > 0 && string(sub.RuleProviders) != "{}" {
		_ = json.Unmarshal(sub.RuleProviders, &b.RuleProviders)
	}
	if sub.TemplateID != nil {
		if t, err := s.Store.GetTemplate(ctx, *sub.TemplateID); err == nil {
			b.Template = &t
		}
	}
	hosts := s.serverHosts(ctx)
	nameByID := map[int64]string{}
	var nodes []domain.Node
	var err error
	switch sub.Kind {
	case domain.SubImported:
		if sub.SourceExternalID == nil {
			return nil, errors.New("导入订阅缺少来源")
		}
		nodes, err = s.Store.ListNodes(ctx, store.NodeFilter{ExternalSubID: sub.SourceExternalID, OnlyEnabled: true})
		if err != nil {
			return nil, err
		}
		if ext, err := s.Store.GetExternal(ctx, *sub.SourceExternalID); err == nil {
			ui := Userinfo{Upload: ext.Upload, Download: ext.Download, Total: ext.Total, Expire: ext.ExpireAt}
			s.applySubQuota(&ui, sub, now)
			b.Userinfo = &ui
		} else {
			b.Userinfo = s.subscriptionUserinfo(ctx, sub, nil, now)
		}
	case domain.SubShare:
		if sub.ShareID == nil {
			return nil, errors.New("分享订阅缺少 share")
		}
		sh, err := s.Store.GetShare(ctx, *sub.ShareID)
		if err != nil {
			return nil, err
		}
		ui := shareUserinfo(sh)
		b.Userinfo = &ui
		if sh.TemplateID != nil && b.Template == nil {
			if t, err := s.Store.GetTemplate(ctx, *sh.TemplateID); err == nil {
				b.Template = &t
			}
		}
		if sh.Status != domain.ShareActive {
			b.InfoNodes = []string{shareStatusLine(sh.Status)}
			return b, nil
		}
		nodes, err = s.Store.ListNodes(ctx, store.NodeFilter{ShareID: sub.ShareID, OnlyEnabled: true})
		if err != nil {
			return nil, err
		}
		if len(sh.ExtraNodeIDs) > 0 {
			extra, err := s.Store.ListNodes(ctx, store.NodeFilter{IDs: sh.ExtraNodeIDs, OnlyEnabled: true})
			if err == nil {
				nodes = append(nodes, extra...)
			}
		}
	case domain.SubGenerated:
		nodes, err = s.SelectNodes(ctx, sub.NodeSelection)
		if err != nil {
			return nil, err
		}
		b.Userinfo = s.subscriptionUserinfo(ctx, sub, nodes, now)
		if b.Userinfo != nil && b.Userinfo.Total > 0 && b.Userinfo.Remaining() == 0 {
			nodes = nil
		}
	}
	if nodes != nil {
		byID := map[int64]domain.Node{}
		var standalones []domain.Node
		for _, n := range nodes {
			byID[n.ID] = n
			if n.Source == domain.NodeChain {
				continue
			}
			standalones = append(standalones, n)
			b.Proxies = append(b.Proxies, ProxyFor(n, hosts))
		}
		b.Proxies = proxynode.DedupeNames(b.Proxies)
		for i, n := range standalones {
			if i < len(b.Proxies) {
				nameByID[n.ID] = b.Proxies[i].Name
			}
		}
		for _, c := range sub.Chains {
			s.appendChain(ctx, b, hosts, byID, nameByID, c.FrontNodeID, c.LandingNodeID, c.Name)
		}
		for _, n := range nodes {
			if n.Source != domain.NodeChain || n.ChainFrontNodeID == nil || *n.ChainFrontNodeID == 0 {
				continue
			}
			s.appendChain(ctx, b, hosts, byID, nameByID, *n.ChainFrontNodeID, n.ID, n.Name)
		}
		if len(sub.ProxyGroups) > 0 && !subscriptionFollowsTemplate(sub) {
			b.Groups = ResolveGroups(sub.ProxyGroups, b.Proxies, b.Chains, nameByID)
		}
	}
	if sub.ShowInfoNodes && b.Userinfo != nil {
		b.InfoNodes = b.Userinfo.InfoLines(now)
	}
	return b, nil
}

func (s *Service) appendChain(ctx context.Context, b *Bundle, hosts map[int64]string, byID map[int64]domain.Node, nameByID map[int64]string, frontID, landingID int64, chainName string) {
	if frontID == 0 || landingID == 0 || frontID == landingID {
		return
	}
	frontName, okF := nameByID[frontID]
	if !okF {
		fn, err := s.Store.GetNode(ctx, frontID)
		if err != nil || !fn.Enabled || fn.Revoked {
			return
		}
		fp := ProxyFor(fn, hosts)
		b.Proxies = append(b.Proxies, fp)
		b.Proxies = proxynode.DedupeNames(b.Proxies)
		frontName = b.Proxies[len(b.Proxies)-1].Name
		nameByID[fn.ID] = frontName
		byID[fn.ID] = fn
		okF = true
	}
	landing, okL := byID[landingID]
	if !okL {
		ln, err := s.Store.GetNode(ctx, landingID)
		if err != nil || !ln.Enabled || ln.Revoked {
			return
		}
		landing = ln
		byID[ln.ID] = ln
		okL = true
	}
	if !okF || !okL {
		return
	}
	if landing.Source == domain.NodeChain {
		for _, n := range byID {
			if n.Source == domain.NodeChain || n.Protocol != landing.Protocol || n.Server != landing.Server || n.Port != landing.Port {
				continue
			}
			landing = n
			break
		}
	}
	landingName := nameByID[landing.ID]
	if landingName == "" {
		landingName = landing.Name
	}
	lp := ProxyFor(landing, hosts)
	lp.Name = strings.TrimSpace(chainName)
	if lp.Name == "" {
		lp.Name = fmt.Sprintf("%s → %s", frontName, landingName)
	}
	for _, c := range b.Chains {
		if c.Via == frontName && c.Proxy.Server == lp.Server && c.Proxy.Port == lp.Port {
			return
		}
	}
	b.Chains = append(b.Chains, ChainedProxy{Proxy: lp, Via: frontName})
}

func shareStatusLine(st domain.ShareStatus) string {
	switch st {
	case SharePausedStatus:
		return "分享已暂停，请联系管理员"
	case domain.ShareExhausted:
		return "本周期流量已用尽"
	case domain.ShareExpired:
		return "分享已到期"
	case domain.ShareRevoked:
		return "分享已被撤销"
	}
	return "分享不可用"
}

// SharePausedStatus alias to keep switch readable.
const SharePausedStatus = domain.SharePaused

func (s *Service) aggregateUserinfo(ctx context.Context, sub domain.Subscription) *Userinfo {
	ui := Userinfo{}
	seen := map[int64]bool{}
	for _, id := range sub.NodeSelection.ExternalSubIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		ext, err := s.Store.GetExternal(ctx, id)
		if err != nil {
			continue
		}
		ui.Upload += ext.Upload
		ui.Download += ext.Download
		ui.Total += ext.Total
		if ext.ExpireAt != nil && (ui.Expire == nil || ext.ExpireAt.Before(*ui.Expire)) {
			ui.Expire = ext.ExpireAt
		}
	}
	return &ui
}

func (s *Service) subscriptionUserinfo(ctx context.Context, sub domain.Subscription, nodes []domain.Node, now time.Time) *Userinfo {
	ui := s.aggregateUserinfo(ctx, sub)
	start := traffic.PeriodStart(now, sub.ResetDay)
	if start.IsZero() {
		start = sub.CreatedAt
		if start.IsZero() {
			start = now.AddDate(0, 0, -30)
		}
	}
	var ids []int64
	for _, n := range nodes {
		if n.ServerID != nil && n.Source == domain.NodeDeployed {
			ids = append(ids, n.ID)
		}
	}
	if up, down, err := s.Store.SumTrafficIDs(ctx, store.SubjectNode, ids, start, now); err == nil {
		ui.Upload += up
		ui.Download += down
	}
	s.applySubQuota(ui, sub, now)
	if ui.Total == 0 && ui.Upload+ui.Download == 0 && ui.Expire == nil {
		return nil
	}
	return ui
}

func (s *Service) applySubQuota(ui *Userinfo, sub domain.Subscription, now time.Time) {
	if ui == nil {
		return
	}
	if sub.TrafficLimitBytes > 0 {
		ui.Total = sub.TrafficLimitBytes
	}
	var cycle *time.Time
	if sub.ResetDay > 0 {
		nr := traffic.NextReset(now, sub.ResetDay)
		cycle = &nr
	}
	for _, t := range []*time.Time{cycle, sub.ExpireAt} {
		if t == nil {
			continue
		}
		if ui.Expire == nil || t.Before(*ui.Expire) {
			ui.Expire = t
		}
	}
}

// Render builds and renders a subscription in the given format.
func (s *Service) Render(ctx context.Context, sub domain.Subscription, format string) (*Rendered, *Bundle, error) {
	b, err := s.Build(ctx, sub)
	if err != nil {
		return nil, nil, err
	}
	r, err := RenderBundle(b, format)
	return r, b, err
}

// RenderBundle dispatches on format.
func RenderBundle(b *Bundle, format string) (result *Rendered, err error) {
	if len(b.Proxies)+len(b.Chains) > 10000 {
		return nil, fmt.Errorf("订阅节点过多")
	}
	for _, p := range b.Proxies {
		if e := proxynode.Validate(p); e != nil {
			return nil, e
		}
	}
	for _, c := range b.Chains {
		if e := proxynode.Validate(c.Proxy); e != nil {
			return nil, e
		}
	}
	defer func() {
		if result != nil && len(result.Body) > 16<<20 {
			result = nil
			err = fmt.Errorf("生成订阅超过大小限制")
		}
	}()

	switch NormalizeFormat(format) {
	case FormatRaw:
		return RenderRaw(b, false)
	case FormatURIList:
		return RenderRaw(b, true)
	case FormatShadowrocket:
		return RenderShadowrocket(b)
	case FormatSurge:
		return RenderSurge(b)
	case FormatSingBox:
		return RenderSingBox(b)
	default:
		return RenderMihomo(b)
	}
}

// SyncStats summarises one external sync.
type SyncStats struct {
	Added   int      `json:"added"`
	Updated int      `json:"updated"`
	Removed int      `json:"removed"`
	Total   int      `json:"total"`
	Errors  []string `json:"errors,omitempty"`
}

// SyncExternal fetches an external subscription and updates its nodes,
// userinfo and traffic history.
func (s *Service) SyncExternal(ctx context.Context, ext domain.ExternalSubscription) (SyncStats, error) {
	res, err := s.Fetcher.Fetch(ctx, ext.URL, ext.UserAgent)
	if err != nil {
		_ = s.Store.RecordExternalSync(ctx, ext.ID, store.ExternalSyncResult{Err: err.Error()})
		return SyncStats{}, err
	}
	return s.applyExternal(ctx, ext, res)
}

// applyExternal persists a fetched/parsed body.
func (s *Service) applyExternal(ctx context.Context, ext domain.ExternalSubscription, res *FetchResult) (SyncStats, error) {
	if len(res.Proxies) == 0 {
		msg := "未解析到任何节点"
		if len(res.ParseErrors) > 0 {
			msg += ": " + strings.Join(res.ParseErrors[:min(3, len(res.ParseErrors))], "; ")
		}
		_ = s.Store.RecordExternalSync(ctx, ext.ID, store.ExternalSyncResult{Err: msg})
		return SyncStats{Errors: res.ParseErrors}, errors.New(msg)
	}
	nodes := make([]domain.Node, 0, len(res.Proxies))
	for _, p := range res.Proxies {
		n := p.ToDomain()
		n.OwnerUserID = ext.OwnerUserID
		nodes = append(nodes, n)
	}
	added, updated, removed, err := s.Store.ReplaceExternalNodes(ctx, ext.ID, nodes)
	if err != nil {
		_ = s.Store.RecordExternalSync(ctx, ext.ID, store.ExternalSyncResult{Err: err.Error()})
		return SyncStats{}, err
	}
	sr := store.ExternalSyncResult{RawContent: res.Body, NodeCount: len(nodes), HasInfo: res.HasUserinfo}
	if res.HasUserinfo {
		sr.Upload, sr.Download, sr.Total, sr.ExpireAt = res.Userinfo.Upload, res.Userinfo.Download, res.Userinfo.Total, res.Userinfo.Expire
		// usage history: delta against last stored totals (upstream resets -> take full)
		dUp := res.Userinfo.Upload - ext.Upload
		dDown := res.Userinfo.Download - ext.Download
		if dUp < 0 {
			dUp = res.Userinfo.Upload
		}
		if dDown < 0 {
			dDown = res.Userinfo.Download
		}
		if ext.LastSyncAt != nil { // skip the very first sync: no baseline
			_ = s.Store.AddTraffic(ctx, store.SubjectExternal, ext.ID, s.Now(), dUp, dDown)
		}
	}
	if err := s.Store.RecordExternalSync(ctx, ext.ID, sr); err != nil {
		return SyncStats{}, err
	}
	return SyncStats{Added: added, Updated: updated, Removed: removed, Total: len(nodes), Errors: res.ParseErrors}, nil
}

// ImportExternalBody applies a manually supplied body (e.g. pasted YAML) to an
// external subscription without fetching.
func (s *Service) ImportExternalBody(ctx context.Context, ext domain.ExternalSubscription, body string) (SyncStats, error) {
	return s.applyExternal(ctx, ext, ParseBody(body))
}

// SyncDue syncs every enabled external subscription whose interval elapsed.
func (s *Service) SyncDue(ctx context.Context, force bool) map[int64]error {
	out := map[int64]error{}
	exts, err := s.Store.ListExternal(ctx)
	if err != nil {
		return out
	}
	now := s.Now()
	for _, e := range exts {
		if !e.Enabled {
			continue
		}
		if !force && e.LastSyncAt != nil && now.Sub(*e.LastSyncAt) < time.Duration(e.SyncIntervalMin)*time.Minute {
			continue
		}
		if _, err := s.SyncExternal(ctx, e); err != nil {
			out[e.ID] = err
		}
	}
	return out
}

// NodeNames returns display names for a set of nodes (for the editor).
func (s *Service) NodeNames(ctx context.Context, ids []int64) map[int64]string {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out
	}
	nodes, err := s.Store.ListNodes(ctx, store.NodeFilter{IDs: ids, IncludeRevoked: true})
	if err != nil {
		return out
	}
	for _, n := range nodes {
		out[n.ID] = n.Name
	}
	return out
}

// SortedFormats returns the supported formats (stable order).
func SortedFormats() []string {
	out := append([]string{}, KnownFormats...)
	sort.Strings(out)
	return out
}
