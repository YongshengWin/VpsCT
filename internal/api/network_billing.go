package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

func (a *API) serverNetworkBilling(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err = a.Store.GetServer(r.Context(), id); err != nil {
		return err
	}
	v, err := a.Store.NetworkBilling(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.OK(w, v)
	return nil
}

func (a *API) updateNetworkBilling(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if err = a.requireNoMaintenance(r, id); err != nil {
		return err
	}
	var in struct {
		ExpectedRevision  int64    `json:"expected_revision"`
		Mode              string   `json:"mode"`
		InterfaceIDs      []string `json:"interface_ids"`
		BoundaryConfirmed bool     `json:"boundary_confirmed"`
	}
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	if !in.BoundaryConfirmed {
		return httpx.BadRequest("请确认所选网卡对应同一个计量层，切换后按入站加出站计费")
	}
	ag, err := a.Store.GetAgentByServer(r.Context(), id)
	if err != nil {
		return err
	}
	var diag agentproto.Diagnostics
	if json.Unmarshal(ag.Diagnostics, &diag) != nil || diag.NetworkBillingVersion < agentproto.NetworkBillingVersion {
		return httpx.E(409, "agent_upgrade_required", "agent 尚不支持计费接口切换，请先升级")
	}
	if ag.LastSeenAt == nil || a.Store.Now().Sub(*ag.LastSeenAt) > 5*time.Minute {
		return httpx.E(409, "agent_offline", "需要在线 agent 才能申请切换计费来源")
	}
	policy := agentproto.NetworkBillingPolicy{Mode: in.Mode, InterfaceIDs: in.InterfaceIDs}
	view, err := a.Store.Network(r.Context(), id)
	if err != nil {
		return err
	}
	if policy.Mode == "interfaces" && (view.ReceivedAt == nil || a.Store.Now().Sub(*view.ReceivedAt) > 5*time.Minute) {
		return httpx.BadRequest("网卡清单已过期，请等待新采样")
	}
	if err = store.ValidateBillingInterfaces(view.Snapshot, policy); err != nil {
		return httpx.BadRequest(err.Error())
	}
	policy, err = a.Store.RequestNetworkBilling(r.Context(), id, in.ExpectedRevision, policy)
	if errors.Is(err, store.ErrBillingConflict) {
		return httpx.E(409, "revision_conflict", err.Error())
	}
	if err != nil {
		return err
	}
	a.audit(r, "server.network_billing.request", strconv.FormatInt(id, 10), map[string]any{"server_id": id, "revision": policy.Revision, "mode": policy.Mode, "interface_ids": policy.InterfaceIDs})
	v, err := a.Store.NetworkBilling(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.OK(w, v)
	return nil
}
