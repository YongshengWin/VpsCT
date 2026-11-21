package api

import (
	"errors"
	"net/http"

	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

func (a *API) listTemplateVersions(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err := a.Store.PersonalTemplateOwner(r.Context(), id); err == nil {
		return httpx.ErrNotFound
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if _, err := a.Store.GetTemplate(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	} else if err != nil {
		return err
	}
	versions, err := a.Store.TemplateVersions(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.OK(w, versions)
	return nil
}

func (a *API) listPersonalTemplateVersions(w http.ResponseWriter, r *http.Request) error {
	t, err := a.personalTemplate(r)
	if err != nil {
		return err
	}
	versions, err := a.Store.TemplateVersions(r.Context(), t.ID)
	if err != nil {
		return err
	}
	httpx.OK(w, versions)
	return nil
}
