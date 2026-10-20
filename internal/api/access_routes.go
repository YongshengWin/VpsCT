package api

import (
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"net/http"
)

// getAccessRoute allows bounded polling and recovery without scanning a list.
func (a *API) getAccessRoute(w http.ResponseWriter, r *http.Request) error {
	id, err := networkOperationID(r)
	if err != nil {
		return err
	}
	route, err := a.Store.AccessRoute(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, route)
	return nil
}

func (a *API) listAccessRoutes(w http.ResponseWriter, r *http.Request) error {
	var id int64
	var err error
	if r.PathValue("id") != "" {
		id, err = httpx.PathInt64(r, "id")
		if err != nil {
			return err
		}
	}
	items, err := a.Store.AccessRoutes(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, items)
	return nil
}
func (a *API) previewAccessRoute(w http.ResponseWriter, r *http.Request) error {
	var in store.AccessRouteRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v, err := a.Store.PreviewAccessRoute(r.Context(), in)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, v)
	return nil
}
func (a *API) createAccessRoute(w http.ResponseWriter, r *http.Request) error {
	var in store.AccessRouteRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v, err := a.Store.CreateAccessRoute(r.Context(), in, a.auditIdentity(r))
	if err != nil {
		return networkOperationError(err)
	}
	httpx.JSON(w, http.StatusAccepted, v)
	return nil
}
func (a *API) stopAccessRoute(w http.ResponseWriter, r *http.Request) error {
	id, err := networkOperationID(r)
	if err != nil {
		return err
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	if err = a.Store.StopAccessRoute(r.Context(), id, in.ExpectedRevision, a.auditIdentity(r)); err != nil {
		return networkOperationError(err)
	}
	v, err := a.Store.AccessRoute(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.JSON(w, http.StatusAccepted, v)
	return nil
}
func (a *API) retryAccessRoute(w http.ResponseWriter, r *http.Request) error {
	id, err := networkOperationID(r)
	if err != nil {
		return err
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	if err = a.Store.RetryAccessRoute(r.Context(), id, in.ExpectedRevision, a.auditIdentity(r)); err != nil {
		return networkOperationError(err)
	}
	v, err := a.Store.AccessRoute(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.JSON(w, http.StatusAccepted, v)
	return nil
}
func (a *API) accessRouteNode(w http.ResponseWriter, r *http.Request) error {
	id, err := networkOperationID(r)
	if err != nil {
		return err
	}
	n, usable, err := a.Subs.AccessRouteNode(r.Context(), id)
	if err != nil {
		return err
	}
	if !usable {
		return httpx.E(409, "route_not_applied", "线路或落地节点尚未应用，暂不能复制接入配置")
	}
	p := subscription.ProxyFor(n, nil)
	uri, err := proxynode.ToURI(p)
	if err != nil {
		return err
	}
	httpx.OK(w, struct {
		Node domain.Node `json:"node"`
		URI  string      `json:"uri"`
	}{n, uri})
	return nil
}
