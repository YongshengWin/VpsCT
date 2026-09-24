package api

import (
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
	"net/http"
)

func (a *API) serverNetwork(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err = a.Store.GetServer(r.Context(), id); err != nil {
		return err
	}
	v, err := a.Store.Network(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.OK(w, v)
	return nil
}

func (a *API) interfaceTraffic(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	iid, err := httpx.PathInt64(r, "iid")
	if err != nil {
		return err
	}
	serverID, err := a.Store.InterfaceServer(r.Context(), iid)
	if err != nil {
		return err
	}
	if id != serverID {
		return httpx.ErrNotFound
	}
	series, err := a.Traffic.Daily(r.Context(), store.SubjectInterface, iid, httpx.QueryInt(r, "days", 30))
	if err != nil {
		return err
	}
	httpx.OK(w, series)
	return nil
}

func (a *API) interfaceHistory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err = a.Store.GetServer(r.Context(), id); err != nil {
		return err
	}
	p, err := a.Store.InterfaceHistory(r.Context(), id, int64(httpx.QueryInt(r, "before", 0)), r.URL.Query().Get("archived") == "true", httpx.QueryInt(r, "limit", 20))
	if err != nil {
		return err
	}
	httpx.OK(w, p)
	return nil
}
func (a *API) archiveInterface(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	iid, err := httpx.PathInt64(r, "iid")
	if err != nil {
		return err
	}
	var in struct {
		Archived *bool `json:"archived"`
	}
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Archived == nil {
		return httpx.BadRequest("须明确是否归档")
	}
	if err = a.Store.ArchiveInterface(r.Context(), id, iid, *in.Archived); err != nil {
		return httpx.BadRequest(err.Error())
	}
	a.audit(r, "network.interface.archive", r.PathValue("iid"), nil)
	httpx.OK(w, map[string]bool{"archived": *in.Archived})
	return nil
}
