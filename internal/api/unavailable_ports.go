package api

import (
	"errors"
	"net/http"

	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

func unavailablePortError(err error) error {
	switch {
	case errors.Is(err, store.ErrUnavailablePort):
		return httpx.BadRequest(err.Error())
	case errors.Is(err, store.ErrUnavailablePortCapacity):
		return httpx.E(409, "unavailable_port_capacity", err.Error())
	default:
		return networkOperationError(err)
	}
}
func unavailablePortID(r *http.Request) (int, error) {
	port, err := httpx.PathInt64(r, "port")
	if err != nil {
		return 0, err
	}
	if port < 1 || port > 65535 {
		return 0, httpx.BadRequest("端口须为 1–65535")
	}
	return int(port), nil
}
func (a *API) listUnavailablePorts(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err = a.Store.GetServer(r.Context(), id); err != nil {
		return networkOperationError(err)
	}
	rows, err := a.Store.UnavailablePorts(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.OK(w, rows)
	return nil
}
func (a *API) getUnavailablePort(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	port, err := unavailablePortID(r)
	if err != nil {
		return err
	}
	record, err := a.Store.UnavailablePort(r.Context(), id, port)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, record)
	return nil
}
func (a *API) saveUnavailablePort(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	port, err := unavailablePortID(r)
	if err != nil {
		return err
	}
	var in struct {
		Transport string `json:"transport"`
		Network   string `json:"network"`
		Reason    string `json:"reason"`
	}
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	record, err := a.Store.SaveUnavailablePort(r.Context(), store.UnavailablePort{ServerID: id, Port: port, Transport: in.Transport, Network: in.Network, Reason: in.Reason}, a.auditIdentity(r))
	if err != nil {
		return unavailablePortError(err)
	}
	httpx.OK(w, record)
	return nil
}
func (a *API) deleteUnavailablePort(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	port, err := unavailablePortID(r)
	if err != nil {
		return err
	}
	if err = a.Store.DeleteUnavailablePort(r.Context(), id, port, a.auditIdentity(r)); err != nil {
		return networkOperationError(err)
	}
	w.WriteHeader(204)
	return nil
}
