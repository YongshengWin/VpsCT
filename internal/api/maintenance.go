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
	if apiKeyFrom(r.Context()) == nil && (err != nil || !sameSiteHost(origin, r.Host) || (origin.Scheme != "https" && origin.Scheme != "http") || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
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
		if normalizeAgentArch(m.Arch) == "" {
			return httpx.E(409, "unsupported_platform", "当前仅支持 Linux AMD64；ARM 安装和升级支持已暂停")
		}
		if in.Version != a.Config.Version {
			return httpx.Conflict("控制端提供的版本已变化，请刷新页面后重试")
		}
		if maintenance.CanonicalReleaseVersion(in.Version) == "" {
			return httpx.Conflict("控制端没有有效发行版本，无法安全校验 agent；请先安装正式发行版")
		}
		spec := a.agentUpdateSpec(m.Arch)
		if spec == nil {
			return httpx.Conflict("缺少对应架构的 agent 发行文件")
		}
		sha = spec.SHA256
	}
	_ = a.Store.ExpireMaintenance(r.Context(), id)
	j := store.MaintenanceJob{Job: maintenance.Job{Request: in.Request, PreviousSHA256: d.BinarySHA256, Status: "queued", Stage: "queued", Message: "等待 agent 接收；15 分钟内未接收会自动过期", CreatedAt: a.Store.Now(), UpdatedAt: a.Store.Now()}, ServerID: id, DeleteServer: in.DeleteServer, ReportToken: auth.RandomToken(32), AgentSHA: sha}
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
	if j.Action == "update" {
		spec, _ := a.maintenanceTarget(r.Context(), j.ServerID)
		if spec == nil || !strings.EqualFold(spec.SHA256, j.AgentSHA) {
			if j.Status == "queued" && spec != nil {
				a.cancelObsoleteAgentUpdate(r.Context(), j)
			}
			return httpx.Conflict("agent 发行目标已变化或暂不可用，旧任务未开始；请等待最新任务或刷新后重试")
		}
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
			if j.Action == "update" && j.Status == "queued" {
				spec, current := a.maintenanceTarget(ctx, serverID)
				if spec != nil && !strings.EqualFold(spec.SHA256, j.AgentSHA) {
					if a.cancelObsoleteAgentUpdate(ctx, j) && current != "" && !strings.EqualFold(current, spec.SHA256) {
						a.queueAutomaticAgentUpdate(ctx, serverID, spec.SHA256)
						return a.nextAgentMaintenance(ctx, serverID)
					}
					continue
				}
			}
			return &agentproto.MaintenanceCommand{Request: j.Request, ReportToken: j.ReportToken, SHA256: j.AgentSHA}
		}
	}
	return nil
}

// Automatic synchronization shares the tracked rollback executor. Only a
// failed download can retry: at most three executions with durable backoff.
func (a *API) queueAutomaticAgentUpdate(ctx context.Context, serverID int64, sha string) {
	version := maintenance.CanonicalReleaseVersion(a.Config.Version)
	if version == "" {
		return
	}
	_ = a.Store.ExpireMaintenance(ctx, serverID)
	jobs, err := a.Store.ListMaintenance(ctx, serverID)
	if err != nil {
		return
	}
	for _, j := range jobs {
		if !j.Active() {
			continue
		}
		if j.Action == "update" && j.Status == "queued" && !strings.EqualFold(j.AgentSHA, sha) {
			if !a.cancelObsoleteAgentUpdate(ctx, j) {
				return
			}
			continue
		}
		return
	}
	previous, err := a.Store.LatestAgentUpdate(ctx, serverID, sha)
	if err != nil && err != store.ErrNotFound {
		return
	}
	if err == nil {
		switch previous.Status {
		case "failed":
			if previous.Stage != "download_retryable" {
				return
			}
			attempts, err := a.Store.AgentUpdateAttempts(ctx, serverID, sha)
			if err != nil || attempts >= 3 {
				return
			}
			backoff := time.Minute
			if attempts >= 2 {
				backoff = 5 * time.Minute
			}
			if a.Store.Now().Sub(previous.UpdatedAt) < backoff {
				return
			}
		case "rolled_back", "interrupted", "queued", "running":
			return
		}
	}
	if attempts, err := a.Store.AgentUpdateAttempts(ctx, serverID, sha); err != nil || attempts >= 3 {
		return
	}
	_, current := a.maintenanceTarget(ctx, serverID)
	j := store.MaintenanceJob{Job: maintenance.Job{Request: maintenance.Request{ID: maintenance.NewID(), Role: "agent", Action: "update", Version: version}, PreviousSHA256: current, Status: "queued", Stage: "queued", Message: "等待自动同步控制端提供的 agent", CreatedAt: a.Store.Now(), UpdatedAt: a.Store.Now()}, ServerID: serverID, ReportToken: auth.RandomToken(32), AgentSHA: sha}
	if err := a.Store.CreateMaintenance(ctx, j); err != nil {
		a.Logger.Warn("queue agent maintenance", "server_id", serverID)
	}
}

func (a *API) maintenanceTarget(ctx context.Context, serverID int64) (*agentproto.AgentUpdateSpec, string) {
	ag, err := a.Store.GetAgentByServer(ctx, serverID)
	if err != nil {
		return nil, ""
	}
	var metrics agentproto.Metrics
	var diagnostics agentproto.Diagnostics
	if json.Unmarshal(ag.Metrics, &metrics) != nil {
		return nil, ""
	}
	_ = json.Unmarshal(ag.Diagnostics, &diagnostics)
	return a.agentUpdateSpec(metrics.Arch), diagnostics.BinarySHA256
}

func (a *API) cancelObsoleteAgentUpdate(ctx context.Context, j store.MaintenanceJob) bool {
	if j.Status != "queued" || j.Action != "update" {
		return false
	}
	j.Status, j.Stage, j.Message = "cancelled", "target_changed", "控制端提供的 agent 已变化，未执行旧任务；等待最新目标同步"
	if err := a.Store.SaveMaintenance(ctx, j, "queued"); err != nil {
		return false
	}
	a.Events.Publish("maintenance.updated", map[string]any{"server_id": j.ServerID, "job": j.Job})
	return true
}

// A fresh heartbeat repairs a lost terminal report after the worker's maximum
// lifetime has elapsed. Never confirm a running worker early: it may still
// roll back. Same-hash reinstalls require the worker's actual completion report.
func (a *API) reconcileAgentMaintenance(ctx context.Context, serverID int64, currentSHA string) {
	if currentSHA == "" {
		return
	}
	if err := a.Store.ExpireMaintenance(ctx, serverID); err != nil {
		return
	}
	j, err := a.Store.LatestAgentUpdate(ctx, serverID, currentSHA)
	if err != nil || j.Status != "interrupted" || j.PreviousSHA256 == "" || strings.EqualFold(j.PreviousSHA256, currentSHA) || a.Store.Now().Sub(j.CreatedAt) <= time.Hour {
		return
	}
	j.Status, j.Stage, j.Message = "succeeded", "heartbeat_confirmed", "已通过恢复后的心跳确认目标 agent 正在运行；节点恢复情况请查看配置及节点状态"
	if err := a.Store.SaveMaintenance(ctx, j, "interrupted"); err != nil {
		return
	}
	a.Events.Publish("maintenance.updated", map[string]any{"server_id": j.ServerID, "job": j.Job})
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
	j, err := a.Store.LatestAgentUpdate(ctx, serverID, sha)
	if err != nil {
		return false
	}
	if j.Status == "succeeded" || j.Active() {
		return false
	}
	if j.Status == "expired" || j.Status == "cancelled" || (j.Status == "failed" && j.Stage == "download_retryable") {
		attempts, err := a.Store.AgentUpdateAttempts(ctx, serverID, sha)
		return err != nil || attempts >= 3
	}
	return true
}
