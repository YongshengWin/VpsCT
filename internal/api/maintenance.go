package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/maintenance"
	"ctlvps/internal/store"
)

type maintenanceInput struct {
	maintenance.Request
	DeleteServer bool   `json:"delete_server"`
	Password     string `json:"password"`
	Code         string `json:"code"`
	Confirm      string `json:"confirm"`
}

// MaintenanceClient allows isolated API tests without opening a root service.
type MaintenanceClient interface {
	Call(context.Context, string, string, any, any) error
}

func (a *API) maintenanceClient() MaintenanceClient {
	if a.Deps.Maintenance != nil {
		return a.Deps.Maintenance
	}
	return maintenance.Client{}
}

func (a *API) maintenanceAuth(w http.ResponseWriter, r *http.Request, in *maintenanceInput) error {
	// Browsers supply Origin for JSON POST. Requiring it here also rejects
	// cross-site forms even if a session cookie happens to be attached.
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host != r.Host || (origin.Scheme != "https" && origin.Scheme != "http") || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return httpx.ErrForbidden
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return httpx.BadRequest("需要 JSON 请求")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := httpx.Decode(r, in); err != nil {
		return err
	}
	if len(in.Password) > 512 || len(in.Code) > 128 || len(in.Confirm) > 256 {
		return httpx.BadRequest("参数过长")
	}
	u := userFrom(r.Context())
	if !a.factorLimiter.AllowN(fmt.Sprintf("maintenance:%d", u.ID), 5) {
		return httpx.E(429, "rate_limited", "操作验证过于频繁，请一分钟后重试")
	}
	if !auth.VerifyPassword(u.PasswordHash, in.Password) {
		return httpx.E(403, "invalid_password", "管理员密码不正确")
	}
	if u.TOTPEnabled {
		before := *u
		if _, ok := a.consumeSecondFactor(u, in.Code); !ok {
			return httpx.E(403, "invalid_code", "两步验证码不正确或已使用")
		}
		if err := a.Store.UpdateUserSecurity(r.Context(), before, *u); err != nil {
			return httpx.Conflict("验证状态已变化，请使用新的验证码")
		}
	}
	return nil
}

func (a *API) controllerMaintenance(w http.ResponseWriter, r *http.Request) error {
	var out maintenance.Info
	err := a.maintenanceClient().Call(r.Context(), "GET", "/status", nil, &out)
	if err != nil {
		out = maintenance.Info{Version: a.Config.Version, Available: false, Reason: err.Error(), Jobs: []maintenance.Job{}}
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) latestController(w http.ResponseWriter, r *http.Request) error {
	var out maintenance.Release
	if err := a.maintenanceClient().Call(r.Context(), "GET", "/latest", nil, &out); err != nil {
		return httpx.E(503, "maintenance_unavailable", err.Error())
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) startControllerMaintenance(w http.ResponseWriter, r *http.Request) error {
	var in maintenanceInput
	if err := a.maintenanceAuth(w, r, &in); err != nil {
		return err
	}
	if in.Role != "controller" || in.DeleteServer {
		return httpx.BadRequest("只能维护当前控制端")
	}
	if err := in.Validate(); err != nil {
		return httpx.BadRequest(err.Error())
	}
	if in.Action == "uninstall" && in.Confirm != "VpsCT" {
		return httpx.BadRequest("请输入 VpsCT 确认卸载控制端")
	}
	if err := a.audit(r, "maintenance.request."+in.Action, "controller", in.Request); err != nil {
		return httpx.E(503, "audit_unavailable", "无法持久化安全审计，未启动任务")
	}
	var j maintenance.Job
	if err := a.maintenanceClient().Call(r.Context(), "POST", "/jobs", in.Request, &j); err != nil {
		return httpx.Conflict(err.Error())
	}
	a.audit(r, "maintenance."+in.Action, "controller", in.Request)
	httpx.JSON(w, 202, j)
	return nil
}

func (a *API) serverMaintenance(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err := a.Store.GetServer(r.Context(), id); err != nil {
		return httpx.ErrNotFound
	}
	_ = a.Store.ExpireMaintenance(r.Context(), id)
	jobs, err := a.Store.ListMaintenance(r.Context(), id)
	if err != nil {
		return err
	}
	ag, err := a.Store.GetAgentByServer(r.Context(), id)
	var d agentproto.Diagnostics
	_ = json.Unmarshal(ag.Diagnostics, &d)
	var m agentproto.Metrics
	_ = json.Unmarshal(ag.Metrics, &m)
	available := err == nil && d.SecurityVersion >= 1 && d.SecurityPolicy && d.Maintenance >= maintenance.Protocol && a.agentStatus(ag) == domain.AgentOnline
	reason := ""
	if !available {
		reason = "需要在线且支持网页维护的 agent；旧版请先按安全迁移文档配置独立信任并更新"
	}
	httpx.OK(w, map[string]any{"available": available, "reason": reason, "version": ag.Version, "target_version": a.Config.Version, "agent_update": a.agentUpdateInfo(r, &m, &d), "jobs": jobs})
	return nil
}

func (a *API) startAgentMaintenance(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	srv, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in maintenanceInput
	if err := a.maintenanceAuth(w, r, &in); err != nil {
		return err
	}
	if in.DeleteServer && in.Action != "uninstall" {
		return httpx.BadRequest("只有卸载 agent 才能同时删除服务器记录")
	}
	if in.Role != "agent" {
		return httpx.BadRequest("只能维护所选服务器的 agent")
	}
	if err := in.Validate(); err != nil {
		return httpx.BadRequest(err.Error())
	}
	if in.Action == "uninstall" && in.Confirm != srv.Name {
		return httpx.BadRequest("请输入完整服务器名称确认卸载")
	}
	if old, err := a.Store.GetMaintenance(r.Context(), in.ID); err == nil {
		if old.ServerID != id || old.Request != in.Request || old.DeleteServer != in.DeleteServer {
			return httpx.Conflict("任务编号已使用")
		}
		httpx.OK(w, old)
		return nil
	}
	ag, err := a.Store.GetAgentByServer(r.Context(), id)
	if err != nil {
		return httpx.Conflict("服务器尚未安装 agent")
	}
	var d agentproto.Diagnostics
	_ = json.Unmarshal(ag.Diagnostics, &d)
	if d.SecurityVersion < 1 || !d.SecurityPolicy || d.Maintenance < maintenance.Protocol || a.agentStatus(ag) != domain.AgentOnline {
		return httpx.Conflict("需要先完成 agent 独立信任配置和安全迁移，并等待它上线")
	}
	var m agentproto.Metrics
	_ = json.Unmarshal(ag.Metrics, &m)
	sha := ""
	if in.Action == "update" {
		if in.Version != a.Config.Version {
			return httpx.Conflict("控制端提供的版本已变化，请刷新页面后重试")
		}
		spec := a.agentUpdateSpec(m.Arch)
		if spec == nil {
			return httpx.Conflict("缺少对应架构的 agent 发行文件")
		}
		sha = spec.SHA256
	}
	_ = a.Store.ExpireMaintenance(r.Context(), id)
	j := store.MaintenanceJob{Job: maintenance.Job{Request: in.Request, Status: "queued", Stage: "queued", Message: "等待 agent 接收；15 分钟内未接收会自动过期", CreatedAt: a.Store.Now(), UpdatedAt: a.Store.Now()}, ServerID: id, DeleteServer: in.DeleteServer, ReportToken: auth.RandomToken(32), AgentSHA: sha}
	if err := a.audit(r, "maintenance.request."+in.Action, fmt.Sprintf("server:%d", id), map[string]any{"request": in.Request, "delete_server": in.DeleteServer}); err != nil {
		return httpx.E(503, "audit_unavailable", "无法持久化安全审计，未启动任务")
	}
	if err := a.Store.CreateMaintenance(r.Context(), j); err != nil {
		return httpx.Conflict("已有维护任务正在执行，请等待当前任务结束")
	}
	a.audit(r, "maintenance."+in.Action, fmt.Sprintf("server:%d", id), map[string]any{"request": in.Request, "delete_server": in.DeleteServer})
	httpx.JSON(w, 202, j)
	return nil
}

// jobBearer authenticates a capability which is bound to one task. It grants
// no access to servers, subscriptions, desired state, or another task.
func (a *API) jobBearer(r *http.Request) (store.MaintenanceJob, error) {
	j, err := a.Store.GetMaintenance(r.Context(), r.PathValue("job"))
	if err != nil {
		return j, httpx.ErrUnauthorized
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || subtle.ConstantTimeCompare([]byte(auth.HashToken(token)), []byte(j.ReportHash)) != 1 || a.Store.Now().Sub(j.CreatedAt) > 24*time.Hour {
		return j, httpx.ErrUnauthorized
	}
	return j, nil
}

func (a *API) claimMaintenance(w http.ResponseWriter, r *http.Request) error {
	j, err := a.jobBearer(r)
	if err != nil {
		return err
	}
	_ = a.Store.ExpireMaintenance(r.Context(), j.ServerID)
	j, err = a.Store.GetMaintenance(r.Context(), j.ID)
	if err != nil {
		return err
	}
	if !j.Active() {
		return httpx.Conflict("任务已结束或过期，不能执行")
	}
	if j.Status == "queued" {
		j.Status, j.Stage, j.Message = "running", "dispatch", "agent 已领取任务，正在启动独立维护进程"
		if err := a.Store.SaveMaintenance(r.Context(), j, "queued"); err != nil {
			return httpx.Conflict("任务已被处理，请重新获取状态")
		}
	}
	httpx.OK(w, map[string]bool{"accepted": true})
	return nil
}

func (a *API) reportMaintenance(w http.ResponseWriter, r *http.Request) error {
	j, err := a.jobBearer(r)
	if err != nil {
		return err
	}
	var report maintenance.Job
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := httpx.Decode(r, &report); err != nil {
		return err
	}
	if report.Request != j.Request || !maintenance.ValidStatus(report.Status) || report.Status == "queued" || report.Status == "expired" || report.Status == "cancelled" || len(report.Message) > 512 || len(report.Stage) > 48 {
		return httpx.BadRequest("维护报告无效")
	}
	if !j.Active() && j.Status != "interrupted" {
		httpx.OK(w, map[string]bool{"accepted": true})
		return nil
	}
	previous := j.Status
	j.Status, j.Stage, j.Message = report.Status, report.Stage, report.Message
	if err := a.Store.SaveMaintenance(r.Context(), j, previous); err != nil {
		return httpx.Conflict("任务状态已变化")
	}
	a.Events.Publish("maintenance.updated", map[string]any{"server_id": j.ServerID, "job": j.Job})
	httpx.OK(w, map[string]bool{"accepted": true})
	return nil
}

func (a *API) nextAgentMaintenance(ctx context.Context, serverID int64) *agentproto.MaintenanceCommand {
	_ = a.Store.ExpireMaintenance(ctx, serverID)
	jobs, err := a.Store.ListMaintenance(ctx, serverID)
	if err != nil {
		return nil
	}
	for _, j := range jobs {
		if j.Active() {
			return &agentproto.MaintenanceCommand{Request: j.Request, ReportToken: j.ReportToken, SHA256: j.AgentSHA}
		}
	}
	return nil
}

// Preserve automatic synchronization, but run new agents through the same
// tracked, rollback-capable executor. A failed target is never retried forever.
func (a *API) queueAutomaticAgentUpdate(ctx context.Context, serverID int64, sha string) {
	_ = a.Store.ExpireMaintenance(ctx, serverID)
	jobs, err := a.Store.ListMaintenance(ctx, serverID)
	if err != nil {
		return
	}
	for _, j := range jobs {
		if j.Active() || (j.Action == "update" && strings.EqualFold(j.AgentSHA, sha)) {
			return
		}
	}
	j := store.MaintenanceJob{Job: maintenance.Job{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "update", Version: a.Config.Version}, Status: "queued", Stage: "queued", Message: "等待自动同步控制端提供的 agent", CreatedAt: a.Store.Now(), UpdatedAt: a.Store.Now()}, ServerID: serverID, ReportToken: auth.RandomToken(32), AgentSHA: sha}
	if err := a.Store.CreateMaintenance(ctx, j); err != nil {
		a.Logger.Warn("queue agent maintenance", "server_id", serverID)
	}
}

func (a *API) requireNoMaintenance(r *http.Request, serverID int64) error {
	_ = a.Store.ExpireMaintenance(r.Context(), serverID)
	jobs, err := a.Store.ListMaintenance(r.Context(), serverID)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Active() {
			return httpx.Conflict("该服务器正在维护，请等待任务结束后再删除记录或重新注册")
		}
	}
	return nil
}

func (a *API) agentMaintenanceNeedsRetry(ctx context.Context, serverID int64, sha string) bool {
	jobs, err := a.Store.ListMaintenance(ctx, serverID)
	if err != nil {
		return false
	}
	for _, j := range jobs {
		if j.Action == "update" && strings.EqualFold(j.AgentSHA, sha) {
			return !j.Active()
		}
	}
	return false
}
