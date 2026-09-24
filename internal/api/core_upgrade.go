package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/corecompat"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

type coreUpgradeServer struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// This is a capability notice, not a latest-release notification. Its identity
// stays stable across panel releases so a dismissed recommendation stays closed.
func (a *API) coreUpgradeStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	pin := strings.TrimSpace(a.Store.GetSetting(ctx, domain.SettingSingBoxVersion, domain.DefaultSingBoxVersion))
	if pin == "" {
		pin = domain.DefaultSingBoxVersion
	}
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		return err
	}
	agents, err := a.Store.ListAgents(ctx)
	if err != nil {
		return err
	}
	byServer := make(map[int64]domain.Agent, len(agents))
	for _, ag := range agents {
		byServer[ag.ServerID] = ag
	}
	out := struct {
		ID                 string              `json:"id"`
		RecommendedVersion string              `json:"recommended_version"`
		PinnedVersion      string              `json:"pinned_version"`
		PinCompatible      bool                `json:"pin_compatible"`
		NeedsAttention     bool                `json:"needs_attention"`
		Servers            []coreUpgradeServer `json:"servers"`
	}{ID: "singbox-ss2022-outbound-" + corecompat.NetworkBaseline, RecommendedVersion: corecompat.NetworkBaseline,
		PinnedVersion: pin, PinCompatible: corecompat.SS2022Outbound(pin), Servers: []coreUpgradeServer{}}
	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		ag := byServer[srv.ID]
		var diag agentproto.Diagnostics
		_ = json.Unmarshal(ag.Diagnostics, &diag)
		var core agentproto.CoreStatus
		for _, c := range diag.Cores {
			if c.Name == "sing-box" {
				core = c
				break
			}
		}
		ds, err := a.Store.LatestDesiredState(ctx, srv.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		var desired agentproto.DesiredState
		if err == nil {
			if err = json.Unmarshal(ds.Payload, &desired); err != nil {
				return err
			}
		}
		used := core.Installed || len(desired.Forwards) > 0
		for _, n := range desired.Nodes {
			used = used || n.Core == string(domain.CoreSingBox)
		}
		if !used {
			continue
		}
		inSync := ds.Revision > 0 && ag.AppliedRevision == ds.Revision && ag.AppliedHash == ds.Hash && ds.Error == "" && desired.Versions["sing-box"].Version == pin
		status, message := assessCoreUpgrade(pin, core, a.agentStatus(ag) == domain.AgentOnline, inSync, ag.ApplyError != "" || diag.SecurityPaused || diag.NetworkGuardError != "")
		out.Servers = append(out.Servers, coreUpgradeServer{srv.ID, srv.Name, core.Version, status, message})
		out.NeedsAttention = out.NeedsAttention || status != "ready"
	}
	httpx.OK(w, out)
	return nil
}

func assessCoreUpgrade(pin string, core agentproto.CoreStatus, online, inSync, failed bool) (string, string) {
	if !online {
		return "offline", "等待服务器上线，暂不能确认实际版本"
	}
	if failed || core.LastError != "" {
		return "error", "服务器回报异常，请在服务器详情检查"
	}
	if !corecompat.SS2022Outbound(pin) {
		return "upgrade", "启用 SS-2022 出口前需选择兼容的官方版本"
	}
	if !core.Installed || core.Version != pin || !inSync {
		return "pending", "等待目标版本安装与配置应用回报"
	}
	if core.Wanted && !core.Active {
		return "error", "内核应运行但尚未启动，请检查服务器详情"
	}
	return "ready", "版本与配置已确认，运行状态正常"
}
