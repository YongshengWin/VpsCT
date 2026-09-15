package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"ctlvps/internal/connlog"
	"ctlvps/internal/domain"
	"ctlvps/internal/geoip"
	"ctlvps/internal/httpx"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
)

// ShareView adds usage and link data.
type ShareView struct {
	domain.Share
	Usage        share.Usage       `json:"usage"`
	Subscription *SubscriptionView `json:"subscription,omitempty"`
	Nodes        []NodeView        `json:"nodes,omitempty"`
	UserName     string            `json:"user_name,omitempty"`
}

func (a *API) shareView(r *http.Request, sh domain.Share, withNodes bool) ShareView {
	v := ShareView{Share: sh, Usage: a.Shares.UsageOf(sh)}
	if sub, err := a.Store.GetSubscriptionByShare(r.Context(), sh.ID); err == nil {
		sv := a.subView(r, sub, true)
		v.Subscription = &sv
	}
	if sh.UserID != nil {
		if u, err := a.Store.GetUser(r.Context(), *sh.UserID); err == nil {
			v.UserName = u.DisplayName()
		}
	}
	if withNodes {
		nodes, _ := a.Store.ListNodes(r.Context(), store.NodeFilter{ShareID: &sh.ID})
		v.Nodes = a.nodeViews(r, nodes)
	}
	return v
}

func (a *API) canSeeShare(u *domain.User, sh domain.Share) bool {
	return isAdmin(u) || (sh.UserID != nil && *sh.UserID == u.ID)
}

func (a *API) listShares(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r.Context())
	var filter *int64
	if !isAdmin(u) {
		filter = &u.ID
	}
	list, err := a.Store.ListShares(r.Context(), filter)
	if err != nil {
		return err
	}
	out := make([]ShareView, 0, len(list))
	for _, sh := range list {
		out = append(out, a.shareView(r, sh, false))
	}
	httpx.OK(w, out)
	return nil
}

type shareInput struct {
	Name           string               `json:"name"`
	UserID         *int64               `json:"user_id"`
	Targets        []domain.ShareTarget `json:"targets"`
	ExtraNodeIDs   []int64              `json:"extra_node_ids"`
	QuotaBytes     int64                `json:"quota_bytes"`
	BillingMode    string               `json:"billing_mode"`
	ResetDay       int                  `json:"reset_day"`
	ExpiresAt      *time.Time           `json:"expires_at"`
	TemplateID     *int64               `json:"template_id"`
	ConnlogEnabled bool                 `json:"connlog_enabled"`
	Notes          string               `json:"notes"`
}

func (in shareInput) apply(sh *domain.Share) error {
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("名称不能为空")
	}
	sh.Name = strings.TrimSpace(in.Name)
	if in.UserID != nil && *in.UserID == 0 {
		sh.UserID = nil
	} else {
		sh.UserID = in.UserID
	}
	sh.Targets = in.Targets
	if sh.Targets == nil {
		sh.Targets = []domain.ShareTarget{}
	}
	for i := range sh.Targets {
		for _, p := range sh.Targets[i].Protocols {
			ok := false
			for _, d := range domain.DeployableProtocols {
				if d == p {
					ok = true
				}
			}
			if !ok {
				return httpx.BadRequest("协议不支持部署: " + p)
			}
		}
	}
	sh.ExtraNodeIDs = in.ExtraNodeIDs
	if sh.ExtraNodeIDs == nil {
		sh.ExtraNodeIDs = []int64{}
	}
	if in.QuotaBytes < 0 {
		return httpx.BadRequest("配额不能为负数")
	}
	sh.QuotaBytes = in.QuotaBytes
	mode, ok := domain.NormalizeBilling(in.BillingMode)
	if !ok {
		return httpx.BadRequest("计费方式无效")
	}
	sh.BillingMode = mode
	if in.ResetDay < 0 || in.ResetDay > 31 {
		return httpx.BadRequest("重置日必须在 0-31 之间（29–31 为每月最后一天）")
	}
	sh.ResetDay = domain.NormalizeResetDay(in.ResetDay)
	if in.ExpiresAt != nil && in.ExpiresAt.IsZero() {
		sh.ExpiresAt = nil
	} else {
		sh.ExpiresAt = in.ExpiresAt
	}
	if in.TemplateID != nil && *in.TemplateID == 0 {
		sh.TemplateID = nil
	} else {
		sh.TemplateID = in.TemplateID
	}
	sh.ConnlogEnabled = in.ConnlogEnabled
	sh.Notes = in.Notes
	return nil
}

func (a *API) createShare(w http.ResponseWriter, r *http.Request) error {
	var in shareInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	sh := domain.Share{}
	if err := in.apply(&sh); err != nil {
		return err
	}
	token, err := a.Shares.Create(r.Context(), &sh)
	if err != nil {
		if sh.ID != 0 {
			// partially created: report but keep record for inspection
			a.Logger.Warn("share created with provisioning error", "share", sh.ID, "err", err)
		} else {
			return err
		}
	}
	a.audit(r, "share.create", sh.Name, map[string]any{"quota": sh.QuotaBytes, "targets": sh.Targets})
	a.Events.Publish("share.changed", map[string]any{"id": sh.ID})
	sh, _ = a.Store.GetShare(r.Context(), sh.ID)
	view := a.shareView(r, sh, true)
	_ = token
	httpx.JSON(w, http.StatusCreated, view)
	return nil
}

func (a *API) getShare(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if !a.canSeeShare(userFrom(r.Context()), sh) {
		return httpx.ErrForbidden
	}
	httpx.OK(w, a.shareView(r, sh, true))
	return nil
}

func (a *API) updateShare(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in shareInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.apply(&sh); err != nil {
		return err
	}
	if err := a.Shares.Update(r.Context(), &sh); err != nil {
		return err
	}
	a.audit(r, "share.update", sh.Name, nil)
	a.Events.Publish("share.changed", map[string]any{"id": sh.ID})
	sh, _ = a.Store.GetShare(r.Context(), id)
	httpx.OK(w, a.shareView(r, sh, true))
	return nil
}

func (a *API) setShareConnlog(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if !a.canSeeShare(userFrom(r.Context()), sh) {
		return httpx.ErrForbidden
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.Shares.SetConnlogEnabled(r.Context(), id, in.Enabled); err != nil {
		return err
	}
	a.audit(r, "share.connlog", sh.Name, map[string]any{"enabled": in.Enabled})
	a.Events.Publish("share.changed", map[string]any{"id": id})
	sh, _ = a.Store.GetShare(r.Context(), id)
	httpx.OK(w, a.shareView(r, sh, true))
	return nil
}

func (a *API) deleteShare(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := a.Shares.Delete(r.Context(), id); err != nil {
		return err
	}
	if a.Connlog != nil {
		_ = a.Connlog.DeleteShare(r.Context(), id)
	}
	a.audit(r, "share.delete", sh.Name, nil)
	a.Events.Publish("share.changed", map[string]any{"id": id, "deleted": true})
	httpx.NoContent(w)
	return nil
}

func (a *API) shareAction(action string) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := httpx.PathInt64(r, "id")
		if err != nil {
			return err
		}
		sh, err := a.Store.GetShare(r.Context(), id)
		if err != nil {
			return httpx.ErrNotFound
		}
		var token string
		switch action {
		case "pause":
			err = a.Shares.Pause(r.Context(), id)
		case "resume":
			err = a.Shares.Resume(r.Context(), id)
		case "reset":
			err = a.Shares.ResetUsage(r.Context(), id)
		case "revoke":
			err = a.Shares.Revoke(r.Context(), id)
		case "reissue":
			token, err = a.Shares.Reissue(r.Context(), id)
		}
		if err != nil {
			return httpx.BadRequest(err.Error())
		}
		a.audit(r, "share."+action, sh.Name, nil)
		a.Events.Publish("share.changed", map[string]any{"id": id})
		sh, _ = a.Store.GetShare(r.Context(), id)
		out := map[string]any{"share": a.shareView(r, sh, true)}
		if token != "" {
			out["token"] = token
		}
		httpx.OK(w, out)
		return nil
	}
}

func (a *API) shareEvents(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if !a.canSeeShare(userFrom(r.Context()), sh) {
		return httpx.ErrForbidden
	}
	list, err := a.Store.ListShareEvents(r.Context(), id, httpx.QueryInt(r, "limit", 100))
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

func (a *API) shareTraffic(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if !a.canSeeShare(userFrom(r.Context()), sh) {
		return httpx.ErrForbidden
	}
	s, err := a.Traffic.Daily(r.Context(), store.SubjectShare, id, httpx.QueryInt(r, "days", 30))
	if err != nil {
		return err
	}
	httpx.OK(w, s)
	return nil
}

// ---- connection logs ----

func parseTimeParam(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return time.Time{}
}

func (a *API) connlogFilter(r *http.Request) connlog.Filter {
	q := r.URL.Query()
	f := connlog.Filter{
		NodeID:   queryInt64Ptr(r, "node_id"),
		ServerID: queryInt64Ptr(r, "server_id"),
		Host:     strings.TrimSpace(q.Get("host")),
		SrcHost:  strings.TrimSpace(q.Get("src")),
		From:     parseTimeParam(q.Get("from")),
		To:       parseTimeParam(q.Get("to")),
		Limit:    httpx.QueryInt(r, "limit", 100),
		Offset:   httpx.QueryInt(r, "offset", 0),
	}
	if q.Get("share_id") == "self" {
		f.SelfOnly = true
	} else {
		f.ShareID = queryInt64Ptr(r, "share_id")
	}
	return f
}

type connEventView struct {
	domain.ConnEvent
	SrcGeo *geoip.Info `json:"src_geo,omitempty"`
}

type clientHitView struct {
	Key  string      `json:"key"`
	Hits int64       `json:"hits"`
	Geo  *geoip.Info `json:"geo,omitempty"`
}

func (a *API) lookupSrc(ip string) *geoip.Info {
	if a.Geo == nil {
		return nil
	}
	return a.Geo.Find(ip)
}

func (a *API) warmSrc(ctx context.Context, ips []string) {
	if a.Geo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	a.Geo.Warm(ctx, ips)
}

func (a *API) queryConnlog(w http.ResponseWriter, r *http.Request) error {
	if a.Connlog == nil {
		return httpx.E(http.StatusServiceUnavailable, "disabled", "连接日志未启用")
	}
	f := a.connlogFilter(r)
	rows, err := a.Connlog.Query(r.Context(), f)
	if err != nil {
		return err
	}
	total, _ := a.Connlog.Count(r.Context(), f)
	ips := make([]string, 0, len(rows))
	for _, e := range rows {
		ips = append(ips, e.SrcHost)
	}
	a.warmSrc(r.Context(), ips)
	events := make([]connEventView, 0, len(rows))
	for _, e := range rows {
		events = append(events, connEventView{ConnEvent: e, SrcGeo: a.lookupSrc(e.SrcHost)})
	}
	httpx.OK(w, map[string]any{"events": events, "total": total})
	return nil
}

func (a *API) topDomains(w http.ResponseWriter, r *http.Request) error {
	if a.Connlog == nil {
		return httpx.E(http.StatusServiceUnavailable, "disabled", "连接日志未启用")
	}
	f := a.connlogFilter(r)
	if f.From.IsZero() {
		days := httpx.QueryInt(r, "days", 7)
		f.From = a.Store.Now().AddDate(0, 0, -days)
	}
	list, err := a.Connlog.GroupCount(r.Context(), f, "dest_host", httpx.QueryInt(r, "limit", 50))
	if err != nil {
		return err
	}
	out := make([]connlog.DomainHit, 0, len(list))
	for _, r := range list {
		out = append(out, connlog.DomainHit{Host: r.Key, Hits: r.Hits})
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) connlogSummary(w http.ResponseWriter, r *http.Request) error {
	if a.Connlog == nil {
		return httpx.E(http.StatusServiceUnavailable, "disabled", "连接日志未启用")
	}
	f := a.connlogFilter(r)
	limit := httpx.QueryInt(r, "limit", 15)
	hosts, err := a.Connlog.GroupCount(r.Context(), f, "dest_host", limit)
	if err != nil {
		return err
	}
	clients, err := a.Connlog.GroupCount(r.Context(), f, "src_host", limit)
	if err != nil {
		return err
	}
	ips := make([]string, 0, len(clients))
	for _, c := range clients {
		ips = append(ips, c.Key)
	}
	a.warmSrc(r.Context(), ips)
	clientViews := make([]clientHitView, 0, len(clients))
	for _, c := range clients {
		clientViews = append(clientViews, clientHitView{Key: c.Key, Hits: c.Hits, Geo: a.lookupSrc(c.Key)})
	}
	nodes, err := a.Connlog.GroupCount(r.Context(), f, "node_id", limit)
	if err != nil {
		return err
	}
	shares, err := a.Connlog.GroupCount(r.Context(), f, "share_id", limit)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"hosts": hosts, "clients": clientViews, "nodes": nodes, "shares": shares})
	return nil
}

func (a *API) exportConnlog(w http.ResponseWriter, r *http.Request) error {
	if a.Connlog == nil {
		return httpx.E(http.StatusServiceUnavailable, "disabled", "连接日志未启用")
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="connlog.csv"`)
	a.audit(r, "connlog.export", "", nil)
	return a.Connlog.ExportCSV(r.Context(), a.connlogFilter(r), w)
}

func (a *API) connlogStats(w http.ResponseWriter, r *http.Request) error {
	if a.Connlog == nil {
		httpx.OK(w, map[string]any{"enabled": false})
		return nil
	}
	st, err := a.Connlog.Stats(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{
		"enabled":                  true,
		"stats":                    st,
		"retention_days":           a.Store.GetSettingInt(r.Context(), domain.SettingConnlogRetention, 7),
		"aggregate_retention_days": a.Store.GetSettingInt(r.Context(), domain.SettingAggRetention, 90),
		"self_enabled":             a.Store.GetSettingBool(r.Context(), domain.SettingConnlogSelf, true),
	})
	return nil
}

func (a *API) deleteShareConnlog(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if a.Connlog != nil {
		if err := a.Connlog.DeleteShare(r.Context(), id); err != nil {
			return err
		}
	}
	a.audit(r, "connlog.delete_share", strconvI(id), nil)
	httpx.NoContent(w)
	return nil
}
