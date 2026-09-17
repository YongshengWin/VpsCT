package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/auth"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/safehttp"
	"ctlvps/internal/store"
)

const agentKey ctxKey = 2

type agentCtx struct {
	Agent  domain.Agent
	Server domain.Server
}

func agentFrom(ctx context.Context) *agentCtx {
	v, _ := ctx.Value(agentKey).(*agentCtx)
	return v
}

// agentRoute wraps a handler with bearer-token agent authentication.
func (a *API) agentRoute(pattern string, h httpx.Handler) {
	a.mux.Handle(pattern, httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		header := r.Header.Get(agentproto.AuthHeader)
		if !strings.HasPrefix(header, "Bearer ") {
			return httpx.ErrUnauthorized
		}
		tok := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if tok == "" || len(tok) > 256 {
			return httpx.ErrUnauthorized
		}
		ag, err := a.Store.GetAgentByTokenHash(r.Context(), auth.HashToken(tok))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrUnauthorized
			}
			a.Logger.Warn("agent lookup failed", "err", err)
			return httpx.E(503, "auth_unavailable", "设备身份验证暂时不可用")
		}
		srv, err := a.Store.GetServer(r.Context(), ag.ServerID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return httpx.ErrUnauthorized
			}
			a.Logger.Warn("agent server lookup failed", "err", err)
			return httpx.E(503, "auth_unavailable", "设备身份验证暂时不可用")
		}
		ctx := context.WithValue(r.Context(), agentKey, &agentCtx{Agent: ag, Server: srv})
		return h(w, r.WithContext(ctx))
	}))
}

func (a *API) agentEnroll(w http.ResponseWriter, r *http.Request) error {
	var in agentproto.EnrollRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ag, err := a.Store.GetAgentByEnrollHash(r.Context(), auth.HashToken(strings.TrimSpace(in.EnrollToken)))
	if err != nil {
		return httpx.E(http.StatusUnauthorized, "invalid_enroll_token", "注册令牌无效或已使用")
	}
	if ag.EnrollExpiresAt != nil && a.Store.Now().After(*ag.EnrollExpiresAt) {
		return httpx.E(http.StatusUnauthorized, "enroll_token_expired", "注册令牌已过期")
	}
	srv, err := a.Store.GetServer(r.Context(), ag.ServerID)
	if err != nil {
		return httpx.ErrNotFound
	}
	token := auth.RandomToken(32)
	if err := a.Store.CompleteEnrollment(r.Context(), ag.ID, ag.EnrollTokenHash, auth.HashToken(token), in.Version); err != nil {
		return err
	}
	metrics, _ := json.Marshal(agentproto.Metrics{Hostname: in.Hostname, Kernel: in.Kernel, Arch: in.Arch})
	_ = a.Store.Heartbeat(r.Context(), ag.ID, in.Version, "", "", metrics, nil)
	_ = a.Store.AddAudit(r.Context(), domain.AuditEvent{Action: "agent.enroll", Target: srv.Name, IP: httpx.ClientIP(r, a.Config.TrustProxy), Detail: mustJSON(map[string]any{"hostname": in.Hostname, "arch": in.Arch, "version": in.Version})})
	a.Events.Publish("agent.enrolled", map[string]any{"server_id": srv.ID})
	if _, _, err := a.Desired.Publish(r.Context(), srv.ID); err != nil {
		a.Logger.Warn("publish after enroll", "err", err)
	}
	httpx.OK(w, agentproto.EnrollResponse{AgentToken: token, ServerID: srv.ID, ServerName: srv.Name, PollIntervalSec: agentproto.DefaultPollIntervalSec})
	return nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (a *API) agentHeartbeat(w http.ResponseWriter, r *http.Request) error {
	ac := agentFrom(r.Context())
	var hb agentproto.Heartbeat
	if err := httpx.Decode(r, &hb); err != nil {
		return err
	}
	if len(hb.Ports) > 4096 || len(hb.Diagnostics.Cores) > 32 || len(hb.Diagnostics.Certs) > 2048 || len(hb.Diagnostics.Warnings) > 64 || len(hb.Diagnostics.RecentErrors) > 64 || len(hb.ApplyError) > 4096 || len(hb.Version) > 128 || len(hb.Epoch) > 256 {
		return httpx.BadRequest("设备上报超出限额")
	}
	if hb.FinalMeters != nil {
		if err := hb.FinalMeters.Validate(); err != nil {
			return httpx.BadRequest("最终计量快照无效")
		}
		result, err := a.Traffic.Ingest(r.Context(), ac.Server, hb)
		if err != nil {
			return err
		}
		if len(result.Shares) > 0 {
			if err = a.Shares.EvaluateDeltas(r.Context(), result.Shares); err != nil {
				a.Logger.Warn("final meter quota evaluation", "err", err)
			}
		}
		a.checkServerQuota(r.Context(), ac.Server)
		httpx.JSON(w, 200, agentproto.HeartbeatResponse{FinalMeterVersion: 1, FinalMeterAck: result.FinalMeterAck, MeteringVersion: 1, ServerTime: a.Store.Now()})
		return nil
	}
	ctx := r.Context()
	ipv4, ipv6 := hb.PublicIPv4, hb.PublicIPv6
	if ipv4 == "" && ipv6 == "" {
		ip := httpx.ClientIP(r, a.Config.TrustProxy)
		if strings.Contains(ip, ":") {
			ipv6 = ip
		} else {
			ipv4 = ip
		}
	}
	if hb.BinarySHA256 != "" && hb.Diagnostics.BinarySHA256 == "" {
		hb.Diagnostics.BinarySHA256 = hb.BinarySHA256
	}
	metrics, _ := json.Marshal(hb.Metrics)
	diag, _ := json.Marshal(hb.Diagnostics)
	if err := a.Store.Heartbeat(ctx, ac.Agent.ID, hb.Version, ipv4, ipv6, metrics, diag); err != nil {
		return err
	}
	if hb.AppliedRevision > 0 || hb.ApplyError != "" {
		_ = a.Store.SetAgentApplied(ctx, ac.Agent.ID, hb.AppliedRevision, hb.AppliedHash, hb.ApplyError)
	}
	// deployed nodes without an explicit public host follow the agent's IP
	if ac.Server.PublicHost == "" && ipv4 != "" {
		nodes, _ := a.Store.ListNodes(ctx, store.NodeFilter{ServerID: &ac.Server.ID, Source: domain.NodeDeployed, IncludeRevoked: true})
		for _, n := range nodes {
			if n.Server != ipv4 {
				n.Server = ipv4
				_ = a.Store.UpdateNode(ctx, &n)
			}
		}
	}
	res, err := a.Traffic.Ingest(ctx, ac.Server, hb)
	if err != nil {
		a.Logger.Warn("traffic ingest", "server", ac.Server.Name, "err", err)
		return err
	} else if len(res.Shares) > 0 {
		if err := a.Shares.EvaluateDeltas(ctx, res.Shares); err != nil {
			a.Logger.Warn("share deltas", "err", err)
		}
	}
	a.checkServerQuota(ctx, ac.Server)
	a.checkDiagnostics(ctx, ac.Server, hb.Diagnostics)

	resp := agentproto.HeartbeatResponse{FinalMeterVersion: 1, MeteringVersion: 1, ServerTime: a.Store.Now(), PollIntervalSec: agentproto.DefaultPollIntervalSec}
	if ds, err := a.Store.LatestDesiredState(ctx, ac.Server.ID); err == nil {
		resp.DesiredRevision, resp.DesiredHash = ds.Revision, ds.Hash
		if d, err := desired.Load(ds); err == nil {
			resp.ConnlogEnabled = d.Connlog.Enabled
		}
	} else {
		// no revision yet: publish one so the agent converges
		if rec, _, err := a.Desired.Publish(ctx, ac.Server.ID); err == nil {
			resp.DesiredRevision, resp.DesiredHash = rec.Revision, rec.Hash
		}
	}
	if spec := a.agentUpdateSpec(hb.Metrics.Arch); spec != nil && hb.Diagnostics.SecurityVersion >= 1 && hb.Diagnostics.SecurityPolicy {
		current := agentReportedSHA(hb, hb.Diagnostics)
		if current != "" && !strings.EqualFold(current, spec.SHA256) {
			if hb.Diagnostics.Maintenance >= 1 {
				a.queueAutomaticAgentUpdate(r.Context(), ac.Server.ID, spec.SHA256)
			} else {
				resp.AgentUpdate = spec
			}
		}
	}
	if hb.Diagnostics.Maintenance >= 1 && hb.Diagnostics.SecurityVersion >= 1 && hb.Diagnostics.SecurityPolicy {
		resp.Maintenance = a.nextAgentMaintenance(r.Context(), ac.Server.ID)
	}
	a.Events.Publish("agent.heartbeat", map[string]any{"server_id": ac.Server.ID, "metrics": hb.Metrics, "applied_revision": hb.AppliedRevision, "desired_revision": resp.DesiredRevision})
	httpx.OK(w, resp)
	return nil
}

// checkServerQuota alerts (and optionally blocks) when a VPS exceeds its quota.
func (a *API) checkServerQuota(ctx context.Context, s domain.Server) {
	if s.QuotaBytes <= 0 || a.Notify == nil {
		return
	}
	u, err := a.Traffic.ServerUsage(ctx, s)
	if err != nil {
		return
	}
	pct := a.Store.GetSettingInt(ctx, domain.SettingQuotaAlertPct, 80)
	if u.Percent >= float64(pct) {
		key := fmt.Sprintf("quota:%d:%s", s.ID, u.PeriodStart.Format("2006-01-02"))
		if u.OverQuota {
			key += ":over"
		}
		a.Notify.SendDedup(ctx, key, 24*time.Hour, fmt.Sprintf("⚠️ %s 本期流量已用 %.1f%% (%s / %s)", s.Name, u.Percent, humanBytes(u.Billed), humanBytes(u.Quota)))
	}
	action := a.Store.GetSetting(ctx, "quota.action", "alert")
	if u.OverQuota && (action == "disable" || action == "stop") && s.Enabled {
		s.Enabled = false
		_ = a.Store.UpdateServer(ctx, &s)
		_, _, _ = a.Desired.Publish(ctx, s.ID)
		_ = a.Store.AddAudit(ctx, domain.AuditEvent{Action: "server.auto_disable", Target: s.Name, Detail: mustJSON(map[string]any{"billed": u.Billed, "quota": u.Quota})})
	}
}

func (a *API) checkDiagnostics(ctx context.Context, s domain.Server, d agentproto.Diagnostics) {
	if a.Notify == nil {
		return
	}
	for _, c := range d.Cores {
		if c.Wanted && !c.Active {
			a.Notify.SendDedup(ctx, fmt.Sprintf("core:%d:%s", s.ID, c.Name), 6*time.Hour, fmt.Sprintf("🔴 %s 上的 %s 未运行: %s", s.Name, c.Name, c.LastError))
		}
		if c.NRestarts >= 5 {
			a.Notify.SendDedup(ctx, fmt.Sprintf("restarts:%d:%s", s.ID, c.Name), 12*time.Hour, fmt.Sprintf("🟠 %s 上的 %s 已重启 %d 次", s.Name, c.Name, c.NRestarts))
		}
	}
	if d.OOMEvents > 0 {
		a.Notify.SendDedup(ctx, fmt.Sprintf("oom:%d", s.ID), 12*time.Hour, fmt.Sprintf("🟠 %s 发生 OOM 事件 %d 次", s.Name, d.OOMEvents))
	}
	if d.ClockSkewMs > 5000 || d.ClockSkewMs < -5000 {
		a.Notify.SendDedup(ctx, fmt.Sprintf("clock:%d", s.ID), 12*time.Hour, fmt.Sprintf("🟠 %s 时钟偏差 %dms，可能影响 Reality/Hy2", s.Name, d.ClockSkewMs))
	}
	for _, c := range d.Certs {
		if !c.NotAfter.IsZero() && time.Until(c.NotAfter) < 7*24*time.Hour {
			a.Notify.SendDedup(ctx, fmt.Sprintf("cert:%d:%s", s.ID, c.Domain), 24*time.Hour, fmt.Sprintf("🟠 %s 证书 %s 将于 %s 过期", s.Name, c.Domain, c.NotAfter.Format("01-02")))
		}
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (a *API) agentDesired(w http.ResponseWriter, r *http.Request) error {
	ac := agentFrom(r.Context())
	rec, err := a.Store.LatestDesiredState(r.Context(), ac.Server.ID)
	if err != nil {
		rec, _, err = a.Desired.Publish(r.Context(), ac.Server.ID)
		if err != nil {
			return err
		}
	}
	if q := r.URL.Query().Get("if_not_revision"); q != "" && q == fmt.Sprint(rec.Revision) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Revision", fmt.Sprint(rec.Revision))
	_, _ = w.Write(rec.Payload)
	return nil
}

func (a *API) agentApplyReport(w http.ResponseWriter, r *http.Request) error {
	ac := agentFrom(r.Context())
	var rep agentproto.ApplyReport
	if err := httpx.Decode(r, &rep); err != nil {
		return err
	}
	st := domain.DesiredApplied
	if rep.Status == "pending" {
		st = domain.DesiredPending
	} else if rep.Status != "applied" {
		st = domain.DesiredFailed
	}
	if err := a.Store.MarkDesiredState(r.Context(), ac.Server.ID, rep.Revision, st, rep.Error); err != nil {
		return err
	}
	if st != domain.DesiredPending {
		if err := a.Store.SetAgentApplied(r.Context(), ac.Agent.ID, rep.Revision, rep.Hash, rep.Error); err != nil {
			return err
		}
	}
	if st == domain.DesiredFailed && a.Notify != nil {
		a.Notify.SendDedup(r.Context(), fmt.Sprintf("apply:%d", ac.Server.ID), time.Hour, fmt.Sprintf("🔴 %s 配置下发失败 (rev %d): %s", ac.Server.Name, rep.Revision, rep.Error))
	}
	a.Events.Publish("agent.applied", map[string]any{"server_id": ac.Server.ID, "revision": rep.Revision, "status": st, "error": rep.Error})
	httpx.NoContent(w)
	return nil
}

func (a *API) agentConnlog(w http.ResponseWriter, r *http.Request) error {
	ac := agentFrom(r.Context())
	lock := &a.batchMu[ac.Agent.ID%64]
	lock.Lock()
	defer lock.Unlock()
	var body io.Reader = http.MaxBytesReader(w, r.Body, 2<<20)
	if strings.Contains(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(body)
		if err != nil {
			return httpx.BadRequest("invalid gzip body")
		}
		defer gz.Close()
		body = gz
	}
	var batch agentproto.ConnlogBatch
	raw, err := safehttp.ReadBounded(body, 8<<20)
	if err != nil {
		return httpx.E(413, "batch_too_large", "日志批次过大")
	}
	if err := json.Unmarshal(raw, &batch); err != nil {
		return httpx.BadRequest("日志 JSON 无效")
	}
	if len(batch.Events) > 10000 {
		return httpx.BadRequest("日志条数过多")
	}
	ack := agentproto.ConnlogAck{AcceptedSeq: batch.Seq, Enabled: a.Connlog != nil}
	if a.Connlog == nil {
		httpx.OK(w, ack)
		return nil
	}
	last, seqErr := a.Store.AgentConnlogSeq(r.Context(), ac.Agent.ID)
	if seqErr != nil {
		return seqErr
	}
	if batch.Seq <= last {
		// duplicate: idempotent ack
		ack.AcceptedSeq = last
		httpx.OK(w, ack)
		return nil
	}
	// owner nodes follow connlog.self_enabled; share nodes follow the share toggle
	nodes, err := a.Store.ListNodes(r.Context(), store.NodeFilter{ServerID: &ac.Server.ID, IncludeRevoked: true})
	if err != nil {
		return err
	}
	selfLog := a.Store.GetSettingBool(r.Context(), domain.SettingConnlogSelf, true)
	shareOf := map[int64]*int64{}
	shares := map[int64]domain.Share{}
	allowed := map[int64]bool{}
	for _, n := range nodes {
		shareOf[n.ID] = n.ShareID
		if n.ShareID == nil {
			allowed[n.ID] = selfLog
			continue
		}
		sh, ok := shares[*n.ShareID]
		if !ok {
			sh, err = a.Store.GetShare(r.Context(), *n.ShareID)
			if err != nil {
				continue
			}
			shares[sh.ID] = sh
		}
		allowed[n.ID] = sh.ConnlogEnabled
	}
	filtered := make([]agentproto.ConnEvent, 0, len(batch.Events))
	for _, e := range batch.Events {
		if allowed[e.NodeID] {
			filtered = append(filtered, e)
		}
	}
	batch.Events = filtered
	if _, err := a.Connlog.Ingest(r.Context(), ac.Server.ID, batch, shareOf); err != nil {
		return err
	}
	_ = a.Store.SetAgentConnlogSeq(r.Context(), ac.Agent.ID, batch.Seq)
	httpx.OK(w, ack)
	return nil
}
