package api

import (
	"net/http"
	"strings"

	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

func (a *API) listExternals(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Store.ListExternal(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

type externalInput struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	UserAgent       string `json:"user_agent"`
	SyncIntervalMin int    `json:"sync_interval_min"`
	Enabled         *bool  `json:"enabled"`
}

func (in externalInput) apply(e *domain.ExternalSubscription) error {
	if strings.TrimSpace(in.Name) == "" {
		return httpx.BadRequest("名称不能为空")
	}
	u := strings.TrimSpace(in.URL)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return httpx.BadRequest("订阅地址必须以 http(s):// 开头")
	}
	e.Name = strings.TrimSpace(in.Name)
	e.URL = u
	e.UserAgent = strings.TrimSpace(in.UserAgent)
	if in.SyncIntervalMin > 0 {
		e.SyncIntervalMin = in.SyncIntervalMin
	} else if e.SyncIntervalMin == 0 {
		e.SyncIntervalMin = 360
	}
	if in.Enabled != nil {
		e.Enabled = *in.Enabled
	}
	return nil
}

func (a *API) createExternal(w http.ResponseWriter, r *http.Request) error {
	var in externalInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	e := domain.ExternalSubscription{Enabled: true, OwnerUserID: userFrom(r.Context()).ID}
	if err := in.apply(&e); err != nil {
		return err
	}
	if err := a.Store.CreateExternal(r.Context(), &e); err != nil {
		return err
	}
	// always attempt a first sync so the operator sees nodes immediately
	var syncErr string
	if _, err := a.Subs.SyncExternal(r.Context(), e); err != nil {
		syncErr = err.Error()
	}
	e, _ = a.Store.GetExternal(r.Context(), e.ID)
	a.audit(r, "external.create", e.Name, nil)
	httpx.JSON(w, http.StatusCreated, map[string]any{"external": e, "sync_error": syncErr})
	return nil
}

func (a *API) updateExternal(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	e, err := a.Store.GetExternal(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in externalInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := in.apply(&e); err != nil {
		return err
	}
	if err := a.Store.UpdateExternal(r.Context(), &e); err != nil {
		return err
	}
	a.audit(r, "external.update", e.Name, nil)
	httpx.OK(w, e)
	return nil
}

func (a *API) deleteExternal(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	e, err := a.Store.GetExternal(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := a.Store.DeleteExternal(r.Context(), id); err != nil {
		return err
	}
	a.audit(r, "external.delete", e.Name, nil)
	httpx.NoContent(w)
	return nil
}

func (a *API) syncExternal(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	e, err := a.Store.GetExternal(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	stats, err := a.Subs.SyncExternal(r.Context(), e)
	if err != nil {
		return httpx.E(http.StatusBadGateway, "sync_failed", err.Error())
	}
	e, _ = a.Store.GetExternal(r.Context(), id)
	httpx.OK(w, map[string]any{"external": e, "stats": stats})
	return nil
}

func (a *API) importExternalBody(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	e, err := a.Store.GetExternal(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	stats, err := a.Subs.ImportExternalBody(r.Context(), e, in.Body)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	e, _ = a.Store.GetExternal(r.Context(), id)
	httpx.OK(w, map[string]any{"external": e, "stats": stats})
	return nil
}

func (a *API) externalTraffic(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	s, err := a.Traffic.Daily(r.Context(), store.SubjectExternal, id, httpx.QueryInt(r, "days", 30))
	if err != nil {
		return err
	}
	httpx.OK(w, s)
	return nil
}
