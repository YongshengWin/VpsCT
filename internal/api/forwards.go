package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"ctlvps/internal/httpx"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func (a *API) listPortForwards(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err = a.Store.GetServer(r.Context(), id); err != nil {
		return networkOperationError(err)
	}
	forwards, err := a.Store.PortForwardViews(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.OK(w, forwards)
	return nil
}

func readPortForwardRequest(r *http.Request, action string, preview, create bool) (store.PortForwardRequest, error) {
	var out store.PortForwardRequest
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return out, err
	}
	var in struct {
		OperationID      string                 `json:"operation_id"`
		Action           string                 `json:"action"`
		ExpectedRevision *int64                 `json:"expected_revision"`
		ExpectedImpact   string                 `json:"expected_impact"`
		Name             string                 `json:"name"`
		Enabled          *bool                  `json:"enabled"`
		Config           *networkconfig.Forward `json:"config"`
	}
	var raw json.RawMessage
	if err = httpx.Decode(r, &raw); err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&in); err != nil {
		return out, httpx.BadRequest("转发请求字段或类型无效")
	}
	if preview {
		action = in.Action
	} else if in.Action != "" {
		return out, httpx.BadRequest("提交操作由请求路径确定")
	}
	if (create && action != "create") || (!create && action != "update" && action != "delete") {
		return out, httpx.BadRequest("转发操作无效")
	}
	if in.ExpectedRevision == nil || (action != "delete" && in.Enabled == nil) {
		return out, httpx.BadRequest("需要明确的启用状态和编辑版本；创建时版本为 0")
	}
	out = store.PortForwardRequest{ID: in.OperationID, Action: action, ExpectedRevision: *in.ExpectedRevision, ExpectedImpact: in.ExpectedImpact, Name: in.Name, Config: in.Config}
	if in.Enabled != nil {
		out.Enabled = *in.Enabled
	}
	if create {
		out.ServerID = id
	} else {
		out.ForwardID = id
	}
	return out, nil
}

func (a *API) previewPortForward(create bool) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		in, err := readPortForwardRequest(r, "", true, create)
		if err != nil {
			return err
		}
		view, err := a.Store.PreviewPortForward(r.Context(), in)
		if err != nil {
			return networkOperationError(err)
		}
		httpx.OK(w, view)
		return nil
	}
}

func (a *API) savePortForward(action string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		in, err := readPortForwardRequest(r, action, false, action == "create")
		if err != nil {
			return err
		}
		op, err := a.Store.RequestReviewedPortForward(r.Context(), in, a.auditIdentity(r))
		if err != nil {
			return networkOperationError(err)
		}
		httpx.JSON(w, http.StatusAccepted, op)
		return nil
	}
}
