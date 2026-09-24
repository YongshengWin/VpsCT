package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/auth"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
	"ctlvps/internal/traffic"
)

// ServerView is a server with its agent and usage.
type ServerView struct {
	domain.Server
	Agent       *domain.Agent           `json:"agent,omitempty"`
	AgentStatus domain.AgentStatus      `json:"agent_status"`
	Metrics     *agentproto.Metrics     `json:"metrics,omitempty"`
	Diagnostics *agentproto.Diagnostics `json:"diagnostics,omitempty"`
	Usage       *traffic.ServerUsage    `json:"usage,omitempty"`
	NodeCount   int                     `json:"node_count"`
	Desired     *desiredSummary         `json:"desired,omitempty"`
	AgentUpdate *AgentUpdateInfo        `json:"agent_update,omitempty"`
}

// AgentUpdateInfo is whether the panel can push a new ctlvps-agent to this VPS.
type AgentUpdateInfo struct {
	Supported bool   `json:"supported"`
	Outdated  bool   `json:"outdated"`
	Current   string `json:"current_sha,omitempty"`
	Latest    string `json:"latest_sha,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Command   string `json:"command,omitempty"`
}

type desiredSummary struct {
	Revision int64                     `json:"revision"`
	Status   domain.DesiredStateStatus `json:"status"`
	InSync   bool                      `json:"in_sync"`
	Error    string                    `json:"error,omitempty"`
}

func (a *API) agentStatus(ag domain.Agent) domain.AgentStatus {
	if ag.TokenHash == "" {
		return domain.AgentPending
	}
	if ag.LastSeenAt == nil {
		return domain.AgentOffline
	}
	offline := time.Duration(a.Store.GetSettingInt(a.ctx(), domain.SettingAgentOfflineSec, 120)) * time.Second
	if time.Since(*ag.LastSeenAt) > offline {
		return domain.AgentOffline
	}
	return domain.AgentOnline
}

func (a *API) serverView(r *http.Request, s domain.Server, withDetail bool) ServerView {
	ctx := r.Context()
	v := ServerView{Server: s, AgentStatus: domain.AgentPending}
	if ag, err := a.Store.GetAgentByServer(ctx, s.ID); err == nil {
		v.Agent = &ag
		v.AgentStatus = a.agentStatus(ag)
		var m agentproto.Metrics
		if json.Unmarshal(ag.Metrics, &m) == nil {
			if m.MemTotal > 0 || m.UptimeSec > 0 {
				v.Metrics = &m
			}
		}
		var d agentproto.Diagnostics
		if json.Unmarshal(ag.Diagnostics, &d) == nil && withDetail {
			v.Diagnostics = &d
		}
		v.AgentUpdate = a.agentUpdateInfo(r, &m, &d)
		if ds, err := a.Store.LatestDesiredState(ctx, s.ID); err == nil {
			v.Desired = &desiredSummary{Revision: ds.Revision, Status: ds.Status, InSync: ag.AppliedRevision == ds.Revision && ag.ApplyError == "", Error: firstNonEmpty(ag.ApplyError, ds.Error)}
		}
	}
	if u, err := a.Traffic.ServerUsage(ctx, s); err == nil {
		v.Usage = &u
	}
	if nodes, err := a.Store.ListNodes(ctx, store.NodeFilter{ServerID: &s.ID}); err == nil {
		v.NodeCount = len(nodes)
	}
	return v
}

func (a *API) agentUpdateInfo(r *http.Request, m *agentproto.Metrics, d *agentproto.Diagnostics) *AgentUpdateInfo {
	base := a.baseURL(r)
	info := &AgentUpdateInfo{Command: officialAgentCommand(base, "", true)}
	if m != nil {
		info.Arch = normalizeAgentArch(m.Arch)
	}
	if d != nil {
		info.Current = d.BinarySHA256
	}
	info.Supported = info.Current != "" && d != nil && d.SecurityVersion >= 1 && d.SecurityPolicy
	if meta, ok := a.agentBinaryMeta(info.Arch); ok {
		info.Latest = meta.SHA256
		info.Outdated = info.Supported && !strings.EqualFold(info.Current, meta.SHA256)
	}
	return info
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) error {
	servers, err := a.Store.ListServers(r.Context())
	if err != nil {
		return err
	}
	out := make([]ServerView, 0, len(servers))
	for _, s := range servers {
		out = append(out, a.serverView(r, s, false))
	}
	httpx.OK(w, out)
	return nil
}

type serverInput struct {
	Name          string   `json:"name"`
	Region        string   `json:"region"`
	PublicHost    string   `json:"public_host"`
	Tags          []string `json:"tags"`
	Notes         string   `json:"notes"`
	QuotaBytes    int64    `json:"quota_bytes"`
	QuotaResetDay int      `json:"quota_reset_day"`
	QuotaBilling  string   `json:"quota_billing"`
	CoreMode      string   `json:"core_mode"`
	IPv4Only      bool     `json:"ipv4_only"`
	CertMode      string   `json:"cert_mode"`
	Enabled       *bool    `json:"enabled"`
}

func (in serverInput) apply(s *domain.Server) error {
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("名称不能为空")
	}
	s.Name = strings.TrimSpace(in.Name)
	s.Region = strings.ToUpper(strings.TrimSpace(in.Region))
	s.PublicHost = strings.TrimSpace(in.PublicHost)
	s.Tags = in.Tags
	s.Notes = in.Notes
	s.QuotaBytes = in.QuotaBytes
	if in.QuotaResetDay < 0 || in.QuotaResetDay > 31 {
		return httpx.BadRequest("重置日必须在 0-31 之间（29–31 为每月最后一天）")
	}
	s.QuotaResetDay = domain.NormalizeResetDay(in.QuotaResetDay)
	mode, ok := domain.NormalizeBilling(in.QuotaBilling)
	if !ok {
		return httpx.BadRequest("计费方式无效")
	}
	s.QuotaBilling = mode
	switch domain.CoreMode(in.CoreMode) {
	case "", domain.CoreModeStable:
		s.CoreMode = domain.CoreModeStable
	case domain.CoreModeLean:
		s.CoreMode = domain.CoreModeLean
	default:
		return httpx.BadRequest("core_mode 无效")
	}
	switch in.CertMode {
	case "", "self_signed":
		s.CertMode = "self_signed"
	case "acme", "external":
		s.CertMode = in.CertMode
	default:
		return httpx.BadRequest("cert_mode 无效")
	}
	s.IPv4Only = in.IPv4Only
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	return nil
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request) error {
	var in serverInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	s := domain.Server{Enabled: true}
	if err := in.apply(&s); err != nil {
		return err
	}
	if err := a.Store.CreateServer(r.Context(), &s); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return httpx.Conflict("服务器名称已存在")
		}
		return err
	}
	_, _, _ = a.Desired.Publish(r.Context(), s.ID)
	a.audit(r, "server.create", s.Name, nil)
	httpx.JSON(w, http.StatusCreated, a.serverView(r, s, true))
	return nil
}

func (a *API) getServer(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	httpx.OK(w, a.serverView(r, s, true))
	return nil
}

func (a *API) updateServer(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in serverInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	prevMode := s.CoreMode
	if err := in.apply(&s); err != nil {
		return err
	}
	if err := a.Store.UpdateServer(r.Context(), &s); err != nil {
		return err
	}
	if prevMode != s.CoreMode {
		// re-assign cores of deployed nodes
		nodes, _ := a.Store.ListNodes(r.Context(), store.NodeFilter{ServerID: &s.ID, IncludeRevoked: true})
		for _, n := range nodes {
			if n.Source == domain.NodeDeployed {
				n.Core = domain.CoreFor(n.Protocol, s.CoreMode)
				_ = a.Store.UpdateNode(r.Context(), &n)
			}
		}
	}
	// deployed nodes inherit the public host
	if err := a.Store.UpdateInheritedNodeHosts(r.Context(), s.ID, s.PublicHost); err != nil {
		return err
	}
	_, _, _ = a.Desired.Publish(r.Context(), s.ID)
	a.audit(r, "server.update", s.Name, nil)
	httpx.OK(w, a.serverView(r, s, true))
	return nil
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if err := a.requireNoMaintenance(r, id); err != nil {
		return err
	}
	s, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := a.Store.DeleteServer(r.Context(), id); err != nil {
		return err
	}
	a.audit(r, "server.delete", s.Name, nil)
	httpx.NoContent(w)
	return nil
}

func (a *API) enrollToken(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if err := a.requireNoMaintenance(r, id); err != nil {
		return err
	}
	s, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	token := auth.RandomToken(32)
	exp := a.Store.Now().Add(15 * time.Minute)
	if err := a.Store.SetAgentEnrollToken(r.Context(), id, auth.HashToken(token), exp.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	base := a.baseURL(r)
	cmd := officialAgentCommand(base, token, false)
	a.audit(r, "server.enroll_token", s.Name, nil)
	httpx.OK(w, map[string]any{"token": token, "expires_at": exp, "install_command": cmd, "server_url": base})
	return nil
}

func (a *API) resetAgentToken(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if err := a.requireNoMaintenance(r, id); err != nil {
		return err
	}
	if err := a.Store.ResetAgentToken(r.Context(), id); err != nil {
		return err
	}
	a.audit(r, "server.reset_agent_token", fmt.Sprint(id), nil)
	httpx.NoContent(w)
	return nil
}

func (a *API) serverTraffic(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	days := httpx.QueryInt(r, "days", 30)
	s, err := a.Traffic.Daily(r.Context(), store.SubjectServer, id, days)
	if err != nil {
		return err
	}
	httpx.OK(w, s)
	return nil
}

func (a *API) serverSamples(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	hours := httpx.QueryInt(r, "hours", 24)
	samples, err := a.Store.ListSamples(r.Context(), id, nil, a.Store.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return err
	}
	// convert cumulative readings to rates (bytes/s)
	type point struct {
		TS     time.Time `json:"ts"`
		RxRate float64   `json:"rx_rate"`
		TxRate float64   `json:"tx_rate"`
	}
	out := []point{}
	for i := 1; i < len(samples); i++ {
		dt := samples[i].TS.Sub(samples[i-1].TS).Seconds()
		if dt <= 0 {
			continue
		}
		drx := samples[i].RxBytes - samples[i-1].RxBytes
		dtx := samples[i].TxBytes - samples[i-1].TxBytes
		if drx < 0 || dtx < 0 {
			continue
		}
		out = append(out, point{TS: samples[i].TS, RxRate: float64(drx) / dt, TxRate: float64(dtx) / dt})
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) serverDesired(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	list, err := a.Store.ListDesiredStates(r.Context(), id, httpx.QueryInt(r, "limit", 10))
	if err != nil {
		return err
	}
	// redact secrets from payloads before returning to the UI
	type view struct {
		domain.DesiredState
		Summary *agentproto.DesiredState `json:"summary,omitempty"`
	}
	out := make([]view, 0, len(list))
	for _, d := range list {
		v := view{DesiredState: d}
		v.Payload = nil
		if ds, err := desired.Load(d); err == nil {
			for i := range ds.Nodes {
				ds.Nodes[i].Params = redact(ds.Nodes[i].Params)
			}
			v.Summary = ds
		}
		out = append(out, v)
	}
	httpx.OK(w, out)
	return nil
}

func redact(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "password") || strings.Contains(lk, "private") || strings.Contains(lk, "psk") || lk == "uuid" {
			out[k] = "••••"
			continue
		}
		out[k] = v
	}
	return out
}

func (a *API) updateAgent(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	info := a.serverView(r, s, false).AgentUpdate
	a.audit(r, "agent.update", s.Name, info)
	out := map[string]any{"agent_update": info}
	if info != nil && info.Supported && info.Outdated && a.agentMaintenanceNeedsRetry(r.Context(), id, info.Latest) {
		out["queued"] = false
		out["message"] = "此版本已有同步记录，请在「agent 维护」查看结果，需要时点击「升级 agent」重试"
	} else if info != nil && info.Supported && info.Outdated {
		out["queued"] = true
		out["message"] = "agent 与控制端提供的版本不同，联网后会随心跳自动更新并重启，无需再次操作"
	} else if info != nil && info.Supported && info.Latest == "" {
		out["queued"] = false
		out["message"] = "控制端尚未提供此架构的 agent，暂时无法检查更新"
	} else if info != nil && info.Supported {
		out["queued"] = false
		out["message"] = "agent 已与控制端提供的版本一致"
	} else {
		out["queued"] = false
		out["manual"] = true
		out["message"] = "需在该 VPS 独立配置可信安装器和签名根，再执行本地安装器迁移；详见安全迁移文档"
	}
	httpx.OK(w, out)
	return nil
}

func (a *API) updateAllAgents(w http.ResponseWriter, r *http.Request) error {
	servers, err := a.Store.ListServers(r.Context())
	if err != nil {
		return err
	}
	var queued, latest, manual, unavailable, retry int
	for _, s := range servers {
		info := a.serverView(r, s, false).AgentUpdate
		if info != nil && info.Supported && info.Outdated && a.agentMaintenanceNeedsRetry(r.Context(), s.ID, info.Latest) {
			retry++
		} else if info != nil && info.Supported && info.Outdated {
			queued++
		} else if info != nil && info.Supported && info.Latest == "" {
			unavailable++
		} else if info != nil && info.Supported {
			latest++
		} else if a.agentStatusMustHave(s) {
			manual++
		}
	}
	a.audit(r, "agent.update_all", "", map[string]any{"queued": queued, "latest": latest, "manual": manual, "unavailable": unavailable})
	httpx.OK(w, map[string]any{
		"queued": queued, "latest": latest, "manual": manual, "unavailable": unavailable,
		"retry":   retry,
		"message": fmt.Sprintf("检查结果：%d 台等待随心跳自动同步，%d 台与控制端版本一致，%d 台需手动更新一次，%d 台缺少分发文件，%d 台请到维护记录查看并按需重试", queued, latest, manual, unavailable, retry),
	})
	return nil
}

func (a *API) agentStatusMustHave(s domain.Server) bool {
	ag, err := a.Store.GetAgentByServer(a.ctx(), s.ID)
	if err != nil {
		return false
	}
	return a.agentStatus(ag) != domain.AgentPending
}

func (a *API) serverRepublish(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	// force a new revision even when unchanged, so the agent re-applies
	s, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	rec, _, err := a.Desired.ForcePublish(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	a.audit(r, "server.republish", s.Name, map[string]any{"revision": rec.Revision})
	httpx.OK(w, map[string]any{"revision": rec.Revision, "hash": rec.Hash})
	return nil
}

type deployInput struct {
	Protocol       string   `json:"protocol"`
	Name           string   `json:"name"`
	Port           int      `json:"port"`
	SNI            string   `json:"sni"`
	Domain         string   `json:"domain"`
	Obfs           bool     `json:"obfs"`
	SnellVersion   int      `json:"snell_version"`
	MieruTransport string   `json:"mieru_transport"`
	CertID         string   `json:"cert_id"`
	CertMode       string   `json:"cert_mode"`
	Tags           []string `json:"tags"`
}

func (a *API) deployNode(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Store.GetServer(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in deployInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	used, err := a.Store.UsedListenPorts(r.Context(), id)
	if err != nil {
		return err
	}
	port := in.Port
	if port == 0 {
		port, err = provision.AllocatePort(used)
		if err != nil {
			return err
		}
	} else if used[port] {
		return httpx.Conflict(fmt.Sprintf("端口 %d 已被占用", port))
	}
	node, err := provision.NewNode(s, "", provision.Options{Name: in.Name, Protocol: in.Protocol, Port: port, SNI: in.SNI, Domain: in.Domain, Obfs: in.Obfs, SnellVersion: in.SnellVersion, MieruTransport: in.MieruTransport, CertMode: in.CertMode, CertID: in.CertID})
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	node.OwnerUserID = userFrom(r.Context()).ID
	if in.Tags != nil {
		node.Tags = in.Tags
	}
	if err := a.Store.CreateNode(r.Context(), &node); err != nil {
		return err
	}
	if _, _, err := a.Desired.Publish(r.Context(), id); err != nil {
		return err
	}
	a.audit(r, "node.deploy", node.Name, map[string]any{"server": s.Name, "protocol": node.Protocol, "port": node.ListenPort})
	httpx.JSON(w, http.StatusCreated, node)
	return nil
}

// Fetch bootstrap code only from the fixed publisher, never from the panel.
// Write the complete script before invoking it so a partial transfer cannot run.
func officialAgentCommand(server, token string, update bool) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	args := " --server " + quote(server)
	if update {
		args += " --update"
	} else {
		args += " --token " + quote(token)
	}
	return `( vpsct_installer=$(mktemp) || exit; trap 'rm -f -- "$vpsct_installer"' EXIT; curl -fLsS --proto '=https' --proto-redir '=https' --max-time 120 https://github.com/YongshengWin/VpsCT/releases/latest/download/install-agent.sh -o "$vpsct_installer" && sudo bash "$vpsct_installer"` + args + ` )`
}
