package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

func strconvI(id int64) string { return strconv.FormatInt(id, 10) }

// ---- dashboard ----

func (a *API) dashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := userFrom(ctx)
	now := a.Store.Now()
	out := map[string]any{"now": now}
	if isAdmin(u) {
		servers, _ := a.Store.ListServers(ctx)
		agents, _ := a.Store.ListAgents(ctx)
		online := 0
		var alerts []map[string]any
		for _, ag := range agents {
			st := a.agentStatus(ag)
			if st == domain.AgentOnline {
				online++
			}
			if st == domain.AgentOffline {
				for _, s := range servers {
					if s.ID == ag.ServerID && s.Enabled {
						alerts = append(alerts, map[string]any{"level": "warn", "kind": "agent_offline", "server_id": s.ID, "message": fmt.Sprintf("%s 的 agent 离线", s.Name)})
					}
				}
			}
			if ag.ApplyError != "" {
				alerts = append(alerts, map[string]any{"level": "error", "kind": "apply_failed", "server_id": ag.ServerID, "message": "配置下发失败: " + ag.ApplyError})
			}
		}
		for _, s := range servers {
			if s.QuotaBytes > 0 {
				if usage, err := a.Traffic.ServerUsage(ctx, s); err == nil && usage.Percent >= float64(a.Store.GetSettingInt(ctx, domain.SettingQuotaAlertPct, 80)) {
					alerts = append(alerts, map[string]any{"level": "warn", "kind": "quota", "server_id": s.ID, "message": fmt.Sprintf("%s 本期流量已用 %.0f%%", s.Name, usage.Percent)})
				}
			}
		}
		nodes, _ := a.Store.ListNodes(ctx, store.NodeFilter{})
		shares, _ := a.Store.ListShares(ctx, nil)
		subs, _ := a.Store.ListSubscriptions(ctx)
		exts, _ := a.Store.ListExternal(ctx)
		activeShares := 0
		for _, sh := range shares {
			if sh.Status == domain.ShareActive {
				activeShares++
			} else if sh.Status == domain.ShareExhausted {
				alerts = append(alerts, map[string]any{"level": "info", "kind": "share_exhausted", "share_id": sh.ID, "message": fmt.Sprintf("分享 %s 流量已用尽", sh.Name)})
			}
		}
		for _, e := range exts {
			if e.LastError != "" {
				alerts = append(alerts, map[string]any{"level": "warn", "kind": "external_sync", "external_id": e.ID, "message": fmt.Sprintf("订阅 %s 同步失败: %s", e.Name, e.LastError)})
			}
		}
		serverSeries, _ := a.Traffic.Daily(ctx, store.SubjectServer, 0, 30)
		extSeries, _ := a.Traffic.Daily(ctx, store.SubjectExternal, 0, 30)
		today := now.Format("2006-01-02")
		var todayUp, todayDown int64
		for _, p := range serverSeries.Points {
			if p.Bucket.Format("2006-01-02") == today {
				todayUp, todayDown = p.Up, p.Down
			}
		}
		out["counts"] = map[string]any{
			"servers": len(servers), "servers_online": online, "nodes": len(nodes), "shares": len(shares), "shares_active": activeShares,
			"subscriptions": len(subs), "externals": len(exts),
		}
		out["traffic"] = map[string]any{
			"servers_30d": serverSeries, "externals_30d": extSeries,
			"today_up": todayUp, "today_down": todayDown,
			"month_up": serverSeries.TotalUp, "month_down": serverSeries.TotalDown,
		}
		if alerts == nil {
			alerts = []map[string]any{}
		}
		out["alerts"] = alerts
		views := make([]ServerView, 0, len(servers))
		for _, s := range servers {
			views = append(views, a.serverView(r, s, false))
		}
		out["servers"] = views
	} else {
		shares, _ := a.Store.ListShares(ctx, &u.ID)
		views := make([]ShareView, 0, len(shares))
		for _, sh := range shares {
			views = append(views, a.shareView(r, sh, false))
		}
		out["shares"] = views
		subs, _ := a.Store.ListSubscriptions(ctx)
		mine := []SubscriptionView{}
		for _, s := range subs {
			if a.canSeeSubscription(u, s) {
				mine = append(mine, a.subView(r, s, true))
			}
		}
		out["subscriptions"] = mine
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) trafficOverview(w http.ResponseWriter, r *http.Request) error {
	days := httpx.QueryInt(r, "days", 30)
	servers, _ := a.Store.ListServers(r.Context())
	type row struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Up     int64  `json:"up"`
		Down   int64  `json:"down"`
		Series any    `json:"series"`
	}
	out := map[string]any{}
	var srows []row
	for _, s := range servers {
		series, err := a.Traffic.Daily(r.Context(), store.SubjectServer, s.ID, days)
		if err != nil {
			continue
		}
		srows = append(srows, row{ID: s.ID, Name: s.Name, Up: series.TotalUp, Down: series.TotalDown, Series: series.Points})
	}
	out["servers"] = srows
	exts, _ := a.Store.ListExternal(r.Context())
	var erows []row
	for _, e := range exts {
		series, err := a.Traffic.Daily(r.Context(), store.SubjectExternal, e.ID, days)
		if err != nil {
			continue
		}
		erows = append(erows, row{ID: e.ID, Name: e.Name, Up: series.TotalUp, Down: series.TotalDown, Series: series.Points})
	}
	out["externals"] = erows
	shares, _ := a.Store.ListShares(r.Context(), nil)
	var shrows []row
	for _, sh := range shares {
		series, err := a.Traffic.Daily(r.Context(), store.SubjectShare, sh.ID, days)
		if err != nil {
			continue
		}
		shrows = append(shrows, row{ID: sh.ID, Name: sh.Name, Up: series.TotalUp, Down: series.TotalDown, Series: series.Points})
	}
	out["shares"] = shrows
	httpx.OK(w, out)
	return nil
}

// ---- settings ----

var editableSettings = map[string]bool{
	domain.SettingSiteName: true, domain.SettingSiteURL: true, domain.SettingShortLinks: true, domain.SettingUserinfoDefault: true,
	domain.SettingTelegramToken: true, domain.SettingTelegramChatID: true, domain.SettingTelegramDaily: true, domain.SettingTelegramHour: true,
	domain.SettingConnlogRetention: true, domain.SettingAggRetention: true, domain.SettingConnlogSelf: true, domain.SettingSampleRetention: true, domain.SettingHourlyRetention: true,
	domain.SettingAccessRetention: true, domain.SettingAgentOfflineSec: true, domain.SettingQuotaAlertPct: true,
	domain.SettingSingBoxVersion: true, domain.SettingSnellVersion: true, domain.SettingMitaVersion: true, "core.mita_sha256": true, domain.SettingRateLimitPerMin: true,
	"core.singbox_sha256": true, "core.snell_sha256": true, "quota.action": true, "site.default_template_id": true,
}

// SettingDefaults are returned when unset.
var SettingDefaults = map[string]string{
	domain.SettingSiteName: defaultSiteName, domain.SettingShortLinks: "1", domain.SettingUserinfoDefault: "1",
	domain.SettingTelegramDaily: "0", domain.SettingTelegramHour: "9",
	domain.SettingConnlogRetention: "7", domain.SettingAggRetention: "90", domain.SettingConnlogSelf: "1", domain.SettingSampleRetention: "48", domain.SettingHourlyRetention: "14",
	domain.SettingAccessRetention: "30", domain.SettingAgentOfflineSec: "120", domain.SettingQuotaAlertPct: "80",
	domain.SettingRateLimitPerMin: "60", "quota.action": "alert",
}

func (a *API) coreVersions(w http.ResponseWriter, r *http.Request) error {
	catalog := a.cores.Get(r.Context())
	httpx.OK(w, catalog)
	return nil
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) error {
	all, err := a.Store.AllSettings(r.Context())
	if err != nil {
		return err
	}
	out := map[string]string{}
	for k, v := range SettingDefaults {
		out[k] = v
	}
	for k, v := range all {
		out[k] = v
	}
	if v := out[domain.SettingTelegramToken]; len(v) > 8 {
		out[domain.SettingTelegramToken] = v[:4] + "••••" + v[len(v)-4:]
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) putSettings(w http.ResponseWriter, r *http.Request) error {
	var in map[string]string
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	changed := []string{}
	values := map[string]string{}
	for k, v := range in {
		if !editableSettings[k] {
			return httpx.BadRequest("不允许修改的设置: " + k)
		}
		if k == domain.SettingTelegramToken && strings.Contains(v, "••••") {
			continue // masked value echoed back
		}
		values[k] = strings.TrimSpace(v)
		changed = append(changed, k)
	}
	if err := a.Store.SetSettings(r.Context(), values); err != nil {
		if errors.Is(err, store.ErrNetworkCoreVersion) {
			return httpx.E(409, "network_core_version", err.Error())
		}
		return err
	}
	for _, k := range changed {
		if k == domain.SettingSingBoxVersion || k == domain.SettingSnellVersion || k == domain.SettingMitaVersion || k == "core.mita_sha256" || k == "core.singbox_sha256" || k == "core.snell_sha256" || k == domain.SettingConnlogSelf {
			_ = a.Desired.PublishAll(r.Context())
			break
		}
	}
	a.audit(r, "settings.update", "", map[string]any{"keys": changed})
	return a.getSettings(w, r)
}

func (a *API) testTelegram(w http.ResponseWriter, r *http.Request) error {
	if a.Notify == nil {
		return httpx.BadRequest("通知未配置")
	}
	if err := a.Notify.Test(r.Context()); err != nil {
		return httpx.BadRequest(err.Error())
	}
	httpx.NoContent(w)
	return nil
}

// ---- audit / access log / bans ----

func (a *API) listAudit(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Store.ListAudit(r.Context(), httpx.QueryInt(r, "limit", 100), httpx.QueryInt(r, "offset", 0))
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

func (a *API) accessLog(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Store.ListAccessLog(r.Context(), queryInt64Ptr(r, "subscription_id"), httpx.QueryInt(r, "limit", 100), httpx.QueryInt(r, "offset", 0))
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

func (a *API) listBans(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Store.ListBans(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

type banInput struct {
	Kind      string     `json:"kind"`
	Value     string     `json:"value"`
	Reason    string     `json:"reason"`
	Enabled   *bool      `json:"enabled"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (in banInput) apply(b *domain.BanRule) error {
	switch in.Kind {
	case "ip":
		if net.ParseIP(strings.TrimSpace(in.Value)) == nil {
			return httpx.BadRequest("IP 地址无效")
		}
	case "cidr":
		if _, _, err := net.ParseCIDR(strings.TrimSpace(in.Value)); err != nil {
			return httpx.BadRequest("CIDR 无效")
		}
	case "ua":
		if strings.TrimSpace(in.Value) == "" {
			return httpx.BadRequest("UA 关键字不能为空")
		}
	default:
		return httpx.BadRequest("kind 必须是 ip/cidr/ua")
	}
	b.Kind = in.Kind
	b.Value = strings.TrimSpace(in.Value)
	b.Reason = in.Reason
	b.Enabled = true
	if in.Enabled != nil {
		b.Enabled = *in.Enabled
	}
	b.ExpiresAt = in.ExpiresAt
	return nil
}

func (a *API) createBan(w http.ResponseWriter, r *http.Request) error {
	var in banInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b := domain.BanRule{}
	if err := in.apply(&b); err != nil {
		return err
	}
	if err := a.Store.CreateBan(r.Context(), &b); err != nil {
		return err
	}
	a.audit(r, "ban.create", b.Value, map[string]any{"kind": b.Kind})
	httpx.JSON(w, http.StatusCreated, b)
	return nil
}

func (a *API) updateBan(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in banInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b := domain.BanRule{ID: id}
	if err := in.apply(&b); err != nil {
		return err
	}
	if err := a.Store.UpdateBan(r.Context(), &b); err != nil {
		return err
	}
	a.audit(r, "ban.update", b.Value, nil)
	httpx.OK(w, b)
	return nil
}

func (a *API) deleteBan(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if err := a.Store.DeleteBan(r.Context(), id); err != nil {
		return err
	}
	a.audit(r, "ban.delete", strconvI(id), nil)
	httpx.NoContent(w)
	return nil
}

// ---- system ----

func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (a *API) systemStatus(w http.ResponseWriter, r *http.Request) error {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	out := map[string]any{
		"version":    a.Config.Version,
		"go":         runtime.Version(),
		"started_at": a.Config.StartedAt,
		"uptime_sec": int64(time.Since(a.Config.StartedAt).Seconds()),
		"goroutines": runtime.NumGoroutine(),
		"heap_bytes": mem.HeapAlloc,
		"data_dir":   a.Config.DataDir,
	}
	if a.Config.DataDir != "" {
		out["data_bytes"] = dirSize(a.Config.DataDir)
	}
	if a.Scheduler != nil {
		out["jobs"] = a.Scheduler.Status()
	}
	if a.Connlog != nil {
		if st, err := a.Connlog.Stats(r.Context()); err == nil {
			out["connlog"] = st
		}
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) meta(w http.ResponseWriter, r *http.Request) error {
	httpx.OK(w, map[string]any{
		"version":   a.Config.Version,
		"site_name": a.Store.GetSetting(r.Context(), domain.SettingSiteName, defaultSiteName),
		"protocols": domain.DeployableProtocols,
		"formats":   []string{"mihomo", "raw", "uri", "surge", "shadowrocket", "singbox"},
		"base_url":  a.baseURL(r),
		"connlog":   a.Connlog != nil,
	})
	return nil
}

// downloadAgent serves prebuilt agent binaries for the install script.
func (a *API) downloadAgent(w http.ResponseWriter, r *http.Request) error {
	platform := r.PathValue("platform")
	if platform != "linux-amd64" && platform != "linux-arm64" {
		return httpx.ErrNotFound
	}
	p := a.findAgentBinary(platform)
	if p != "" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="ctlvps-agent"`)
		http.ServeFile(w, r, p)
		return nil
	}
	return httpx.E(http.StatusNotFound, "agent_binary_missing", "服务端未附带 agent 二进制，请设置 --agent-bin-dir 或放入 data/agents/")
}

// ---- SSE ----

func (a *API) events(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.E(500, "no_stream", "streaming unsupported")
	}
	u := userFrom(r.Context())
	a.streamMu.Lock()
	if a.streams == nil {
		a.streams = map[int64]int{}
	}
	if a.streamTotal >= 100 || a.streams[u.ID] >= 5 {
		a.streamMu.Unlock()
		return httpx.E(429, "stream_limit", "实时连接过多")
	}
	a.streams[u.ID]++
	a.streamTotal++
	a.streamMu.Unlock()
	defer func() {
		a.streamMu.Lock()
		a.streams[u.ID]--
		a.streamTotal--
		if a.streams[u.ID] == 0 {
			delete(a.streams, u.ID)
		}
		a.streamMu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	send := func(s string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprint(w, s); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send("event: hello\ndata: {}\n\n") {
		return nil
	}
	ch, cancel := a.Events.Subscribe()
	defer cancel()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-ping.C:
			var err error
			u, err = a.currentUser(r)
			if err != nil {
				return nil
			}
			if !send(": ping\n\n") {
				return nil
			}
		case ev := <-ch:
			current, err := a.currentUser(r)
			if err != nil {
				return nil
			}
			u = current
			if !isAdmin(u) {
				if ev.Type != "share.changed" {
					continue
				}
				data, ok := ev.Data.(map[string]any)
				if !ok {
					continue
				}
				id, ok := data["id"].(int64)
				if !ok {
					continue
				}
				sh, err := a.Store.GetShare(r.Context(), id)
				if err != nil || !a.canSeeShare(u, sh) {
					continue
				}
			}
			b, _ := json.Marshal(ev.Data)
			if !send(fmt.Sprintf("event: %s\ndata: %s\n\n", ev.Type, b)) {
				return nil
			}
		}
	}
}
