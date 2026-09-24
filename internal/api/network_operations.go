package api

import (
	"errors"
	"net/http"

	"ctlvps/internal/httpx"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func networkOperationError(err error) error {
	switch {
	case errors.Is(err, store.ErrManagedTransit):
		return httpx.E(409, "managed_transit", err.Error())
	case errors.Is(err, store.ErrTransitRequest):
		return httpx.BadRequest(err.Error())
	case errors.Is(err, store.ErrNetworkImpactRequired):
		return httpx.E(428, "network_preview_required", err.Error())
	case errors.Is(err, store.ErrNetworkImpactChanged):
		return httpx.E(409, "network_impact_changed", err.Error())
	case errors.Is(err, store.ErrNetworkImpactCapacity):
		return httpx.E(409, "network_impact_capacity", err.Error())
	case errors.Is(err, store.ErrEgressRequest):
		return httpx.BadRequest(err.Error())
	case errors.Is(err, store.ErrForwardRequest):
		return httpx.BadRequest(err.Error())
	case errors.Is(err, store.ErrListenPortConflict):
		return httpx.E(409, "listen_port_conflict", err.Error())
	case errors.Is(err, store.ErrForwardCapacity), errors.Is(err, store.ErrForwardRetired):
		return httpx.E(409, "forward_conflict", err.Error())
	case errors.Is(err, store.ErrEgressInUse):
		return httpx.E(409, "egress_in_use", err.Error())
	case errors.Is(err, store.ErrEgressCapacity):
		return httpx.E(409, "egress_capacity", err.Error())
	case errors.Is(err, store.ErrNotFound):
		return httpx.ErrNotFound
	case errors.Is(err, store.ErrNetworkMaintenance):
		return httpx.E(409, "maintenance_in_progress", err.Error())
	case errors.Is(err, store.ErrNetworkRetryConflict), errors.Is(err, store.ErrNetworkOperationConflict), errors.Is(err, store.ErrNetworkConflict):
		return httpx.E(409, "network_conflict", err.Error())
	case errors.Is(err, store.ErrNetworkOperationCapacity):
		return httpx.E(409, "network_operation_capacity", err.Error())
	case errors.Is(err, store.ErrNetworkNotReady):
		return httpx.E(409, "network_not_ready", err.Error())
	case errors.Is(err, store.ErrNetworkCoreVersion):
		return httpx.E(409, "network_core_version", err.Error())
	default:
		return err
	}
}

func networkOperationID(r *http.Request) (string, error) {
	id := r.PathValue("op")
	if !networkconfig.ValidIdentity(id) {
		return "", httpx.BadRequest("非法的操作编号")
	}
	return id, nil
}

func (a *API) serverNetworkOperations(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if _, err = a.Store.GetServer(r.Context(), id); err != nil {
		return networkOperationError(err)
	}
	operations, err := a.Store.NetworkOperations(r.Context(), id, httpx.QueryInt(r, "limit", 30), httpx.QueryInt(r, "offset", 0))
	if err != nil {
		return err
	}
	httpx.OK(w, operations)
	return nil
}

func (a *API) getNetworkOperation(w http.ResponseWriter, r *http.Request) error {
	id, err := networkOperationID(r)
	if err != nil {
		return err
	}
	op, err := a.Store.NetworkOperation(r.Context(), id)
	if err != nil {
		return networkOperationError(err)
	}
	httpx.OK(w, op)
	return nil
}

func (a *API) retryNetworkOperation(w http.ResponseWriter, r *http.Request) error {
	id, err := networkOperationID(r)
	if err != nil {
		return err
	}
	var in struct {
		ExpectedRetryRevision *int64 `json:"expected_retry_revision"`
	}
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.ExpectedRetryRevision == nil {
		return httpx.BadRequest("缺少操作重试版本，请刷新后重试")
	}
	op, err := a.Store.RetryNetworkOperation(r.Context(), id, *in.ExpectedRetryRevision, a.auditIdentity(r))
	if err != nil {
		return networkOperationError(err)
	}
	// The periodic outbox worker owns publication. A disconnected browser or
	// process exit after this response cannot lose the accepted retry.
	httpx.JSON(w, http.StatusAccepted, op)
	return nil
}
