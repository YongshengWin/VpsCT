package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"ctlvps/internal/httpx"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func decodeEgressBody(r *http.Request, v any) error {
	var raw json.RawMessage
	if err := httpx.Decode(r, &raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return httpx.BadRequest("出口请求字段或类型无效")
	}
	return nil
}

func (a *API) listEgressProfiles(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err := a.Store.GetServer(r.Context(), id); err != nil {
		return networkOperationError(err)
	}
	profiles, err := a.Store.ListEgressProfiles(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.OK(w, profiles)
	return nil
}

func (a *API) getEgressProfile(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	view, err := a.Store.EgressProfileView(r.Context(), id, httpx.QueryInt(r, "limit", 50), httpx.QueryInt(r, "offset", 0))
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, view)
	return nil
}

func (a *API) getEgressRevision(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	revision, err := httpx.PathInt64(r, "revision")
	if err != nil {
		return err
	}
	p, err := a.Store.GetEgressProfile(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	v, err := a.Store.GetEgressRevision(r.Context(), p.ServerID, id, revision)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, v)
	return nil
}

func (a *API) createEgressProfile(w http.ResponseWriter, r *http.Request) error {
	return a.saveEgressProfile(w, r, "create")
}
func (a *API) updateEgressProfile(w http.ResponseWriter, r *http.Request) error {
	return a.saveEgressProfile(w, r, "update")
}
func (a *API) saveEgressProfile(w http.ResponseWriter, r *http.Request, action string) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		OperationID      string                           `json:"operation_id"`
		ExpectedRevision *int64                           `json:"expected_revision"`
		Name             string                           `json:"name"`
		Kind             string                           `json:"kind"`
		Enabled          *bool                            `json:"enabled"`
		Config           json.RawMessage                  `json:"config"`
		ExpectedImpact   string                           `json:"expected_impact"`
		Credentials      *networkconfig.SOCKS5Credentials `json:"credentials,omitempty"`
	}
	if err := decodeEgressBody(r, &in); err != nil {
		return err
	}
	if in.Enabled == nil || in.ExpectedRevision == nil {
		return httpx.BadRequest("需要明确的启用状态和出口编辑版本；创建时版本为 0")
	}
	if in.Kind != "direct" && in.Kind != "socks5" && in.Kind != "ssh" && in.Kind != "wireguard" {
		return httpx.BadRequest("不支持该出口类型")
	}
	req := store.EgressProfileRequest{ID: in.OperationID, Action: action, ExpectedRevision: *in.ExpectedRevision, Name: in.Name, Kind: in.Kind, Enabled: *in.Enabled, Config: in.Config, ExpectedImpact: in.ExpectedImpact, Credentials: in.Credentials}
	if action == "create" {
		req.ServerID = id
	} else {
		req.ProfileID = id
	}
	op, err := a.Store.RequestReviewedEgressProfile(r.Context(), req, a.auditIdentity(r))
	if err != nil {
		return networkOperationError(err)
	}
	httpx.JSON(w, http.StatusAccepted, op)
	return nil
}

func (a *API) deleteEgressProfile(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		OperationID      string `json:"operation_id"`
		ExpectedRevision *int64 `json:"expected_revision"`
		ExpectedImpact   string `json:"expected_impact"`
	}
	if err := decodeEgressBody(r, &in); err != nil {
		return err
	}
	if in.ExpectedRevision == nil {
		return httpx.BadRequest("缺少出口编辑版本")
	}
	op, err := a.Store.RequestReviewedEgressProfile(r.Context(), store.EgressProfileRequest{ID: in.OperationID, Action: "delete", ProfileID: id, ExpectedRevision: *in.ExpectedRevision, ExpectedImpact: in.ExpectedImpact}, a.auditIdentity(r))
	if errors.Is(err, store.ErrEgressInUse) {
		view, readErr := a.Store.EgressProfileView(r.Context(), id, 100, 0)
		if readErr != nil {
			return networkOperationError(err)
		}
		httpx.JSON(w, http.StatusConflict, map[string]any{"error": httpx.Error{Code: "egress_in_use", Message: err.Error()}, "references": view.References, "reference_count": view.ReferenceCount})
		return nil
	}
	if err != nil {
		return networkOperationError(err)
	}
	httpx.JSON(w, http.StatusAccepted, op)
	return nil
}

func (a *API) previewEgressCreate(w http.ResponseWriter, r *http.Request) error {
	return a.previewEgress(w, r, true)
}
func (a *API) previewEgressProfile(w http.ResponseWriter, r *http.Request) error {
	return a.previewEgress(w, r, false)
}
func (a *API) previewEgress(w http.ResponseWriter, r *http.Request, create bool) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Action           string                           `json:"action"`
		ExpectedRevision *int64                           `json:"expected_revision"`
		Name             string                           `json:"name"`
		Kind             string                           `json:"kind"`
		Enabled          *bool                            `json:"enabled"`
		Config           json.RawMessage                  `json:"config"`
		Credentials      *networkconfig.SOCKS5Credentials `json:"credentials,omitempty"`
	}
	if err = decodeEgressBody(r, &in); err != nil {
		return err
	}
	if in.ExpectedRevision == nil || (in.Action != "delete" && in.Enabled == nil) {
		return httpx.BadRequest("需要明确的启用状态和出口编辑版本")
	}
	if (create && in.Action != "create") || (!create && in.Action != "update" && in.Action != "delete") {
		return httpx.BadRequest("出口预览操作无效")
	}
	if in.Action != "delete" && in.Kind != "direct" && in.Kind != "socks5" && in.Kind != "ssh" && in.Kind != "wireguard" {
		return httpx.BadRequest("不支持该出口类型")
	}
	req := store.EgressProfileRequest{Action: in.Action, ExpectedRevision: *in.ExpectedRevision, Name: in.Name, Kind: in.Kind, Config: in.Config, Credentials: in.Credentials}
	if in.Enabled != nil {
		req.Enabled = *in.Enabled
	}
	if create {
		req.ServerID = id
	} else {
		req.ProfileID = id
	}
	view, err := a.Store.PreviewEgressProfile(r.Context(), req)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, view)
	return nil
}
