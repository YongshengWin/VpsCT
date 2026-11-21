package api

import (
	"errors"
	"net/http"

	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

func (a *API) protectControllerOwner(r *http.Request, targetID int64) error {
	ownerID, err := a.Store.ControllerOwner(r.Context())
	if err != nil {
		return err
	}
	if targetID == ownerID && userFrom(r.Context()).ID != ownerID {
		return httpx.ErrForbidden
	}
	return nil
}

func (a *API) controllerOwner(w http.ResponseWriter, r *http.Request) error {
	id, err := a.Store.ControllerOwner(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"user_id": id, "is_owner": userFrom(r.Context()).ID == id})
	return nil
}

type ownerTransferInput struct {
	UserID   int64  `json:"user_id"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (a *API) transferControllerOwner(w http.ResponseWriter, r *http.Request) error {
	var in ownerTransferInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ownerID, err := a.Store.ControllerOwner(r.Context())
	if err != nil {
		return err
	}
	if ownerID != userFrom(r.Context()).ID {
		return httpx.ErrForbidden
	}
	if err := a.permissionReauth(w, r, in.Password, in.Code); err != nil {
		return err
	}
	if err := a.Store.TransferControllerOwner(r.Context(), *userFrom(r.Context()), in.UserID, a.auditIdentity(r)); err != nil {
		if errors.Is(err, store.ErrPermissionDenied) {
			return httpx.Conflict("所有者已变化，或目标不是启用的管理员")
		}
		return err
	}
	httpx.OK(w, map[string]any{"user_id": in.UserID})
	return nil
}
