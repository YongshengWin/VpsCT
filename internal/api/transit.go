package api

import (
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
	"net/http"
)

func (a *API) listManagedTransits(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	items, err := a.Store.ManagedTransits(r.Context(), id)
	if err != nil {
		return err
	}
	if r.URL.Query().Get("include_hidden") != "1" {
		visible := items[:0]
		for _, item := range items {
			if !item.Hidden {
				visible = append(visible, item)
			}
		}
		items = visible
	}
	httpx.OK(w, items)
	return nil
}
func (a *API) setManagedTransitVisibility(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("op")
	var in struct {
		ExpectedUpdatedAt string `json:"expected_updated_at"`
		Hidden            bool   `json:"hidden"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.Store.SetManagedTransitHidden(r.Context(), id, in.ExpectedUpdatedAt, in.Hidden); err != nil {
		return networkOperationError(err)
	}
	item, err := a.Store.ManagedTransit(r.Context(), id)
	if err != nil {
		return err
	}
	a.audit(r, "transit.visibility", id, map[string]any{"hidden": in.Hidden})
	httpx.OK(w, item)
	return nil
}
func (a *API) previewManagedTransit(w http.ResponseWriter, r *http.Request) error {
	var in store.TransitRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	view, err := a.Store.PreviewManagedTransit(r.Context(), in)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	httpx.OK(w, view)
	return nil
}
func (a *API) createManagedTransit(w http.ResponseWriter, r *http.Request) error {
	var in store.TransitRequest
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	v, err := a.Store.CreateManagedTransit(r.Context(), in, a.auditIdentity(r))
	if err != nil {
		return networkOperationError(err)
	}
	httpx.JSON(w, http.StatusAccepted, v)
	return nil
}
func (a *API) retireManagedTransit(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("op")
	var in struct {
		ExpectedImpact string `json:"expected_impact"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.Store.RetireManagedTransit(r.Context(), id, in.ExpectedImpact); err != nil {
		return networkOperationError(err)
	}
	v, err := a.Store.ManagedTransit(r.Context(), id)
	if err != nil {
		return err
	}
	a.audit(r, "transit.retire", id, nil)
	httpx.JSON(w, http.StatusAccepted, v)
	return nil
}
func (a *API) previewTransitRetirement(w http.ResponseWriter, r *http.Request) error {
	v, err := a.Store.PreviewTransitRetirement(r.Context(), r.PathValue("op"))
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, v)
	return nil
}
func (a *API) transitTraffic(w http.ResponseWriter, r *http.Request) error {
	t, err := a.Store.ManagedTransit(r.Context(), r.PathValue("op"))
	if err != nil {
		return err
	}
	series, err := a.Traffic.Daily(r.Context(), "transit", t.LandingNodeID, httpx.QueryInt(r, "days", 30))
	if err != nil {
		return err
	}
	httpx.OK(w, series)
	return nil
}

func (a *API) retryManagedTransit(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("op")
	var in struct {
		ExpectedUpdatedAt string `json:"expected_updated_at"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.Store.RetryManagedTransit(r.Context(), id, in.ExpectedUpdatedAt); err != nil {
		return networkOperationError(err)
	}
	v, err := a.Store.ManagedTransit(r.Context(), id)
	if err != nil {
		return err
	}
	a.audit(r, "transit.retry", id, nil)
	httpx.JSON(w, http.StatusAccepted, v)
	return nil
}
