package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func (a *API) serverNetworkCapabilities(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	coreKind := domain.Core(r.URL.Query().Get("core"))
	if coreKind == "" {
		coreKind = domain.CoreSingBox
	}
	if coreKind != domain.CoreSingBox && coreKind != domain.CoreSnell && coreKind != domain.CoreMita {
		return httpx.BadRequest("不支持的内核")
	}
	view, err := a.Store.NetworkCapabilitiesForCore(r.Context(), id, coreKind)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, view)
	return nil
}

func (a *API) previewNodeNetwork(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Network       json.RawMessage `json:"network"`
		AdvertiseHost *string         `json:"advertise_host"`
	}
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	policy, err := networkconfig.DecodeNode(in.Network)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	if in.AdvertiseHost != nil {
		if policy == nil || policy.AdvertiseMode != "override" {
			return httpx.BadRequest("仅独立访问地址模式可以指定访问地址")
		}
		if err := networkconfig.ValidateAdvertiseHost(*in.AdvertiseHost); err != nil {
			return httpx.BadRequest(err.Error())
		}
	}
	n, err := a.Store.GetNode(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	if n.Source != domain.NodeDeployed || n.ServerID == nil {
		return httpx.BadRequest("只有受管部署节点可以设置服务器网络")
	}
	view, err := a.Store.ReviewNodeNetwork(r.Context(), id, policy, in.AdvertiseHost)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, view)
	return nil
}

func (a *API) updateNodeNetwork(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		OperationID      string          `json:"operation_id"`
		ExpectedRevision *int64          `json:"expected_revision"`
		Network          json.RawMessage `json:"network"`
		AdvertiseHost    *string         `json:"advertise_host"`
		ExpectedImpact   string          `json:"expected_impact"`
	}
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	if !networkconfig.ValidIdentity(in.OperationID) || in.ExpectedRevision == nil || *in.ExpectedRevision < 0 || *in.ExpectedRevision >= 1<<53-1 {
		return httpx.BadRequest("需要有效的操作编号和节点网络编辑版本")
	}
	policy, err := networkconfig.DecodeNode(in.Network)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	if in.AdvertiseHost != nil {
		if policy == nil || policy.AdvertiseMode != "override" {
			return httpx.BadRequest("仅独立访问地址模式可以指定访问地址")
		}
		if err := networkconfig.ValidateAdvertiseHost(*in.AdvertiseHost); err != nil {
			return httpx.BadRequest(err.Error())
		}
	}
	request := store.NodeNetworkRequest{ID: in.OperationID, NodeID: id, ExpectedRevision: *in.ExpectedRevision, Network: policy, AdvertiseHost: in.AdvertiseHost, ExpectedImpact: in.ExpectedImpact}
	if existing, err := a.Store.ExistingNodeNetworkRequest(r.Context(), request); err == nil {
		httpx.JSON(w, http.StatusAccepted, existing)
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return networkOperationError(err)
	}
	n, err := a.Store.GetNode(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	if n.Source != domain.NodeDeployed || n.ServerID == nil {
		return httpx.BadRequest("只有受管部署节点可以设置服务器网络")
	}
	op, err := a.Store.RequestReviewedNodeNetwork(r.Context(), request, a.auditIdentity(r))
	if err != nil {
		return networkOperationError(err)
	}
	httpx.JSON(w, http.StatusAccepted, op)
	return nil
}
