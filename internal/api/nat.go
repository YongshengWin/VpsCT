package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"ctlvps/internal/httpx"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/store"
)

type ServerNATSummary struct {
	Supported  bool   `json:"supported"`
	Source     string `json:"source"`
	Configured bool   `json:"configured"`
	Status     string `json:"status"`
	Editable   bool   `json:"editable"`
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
	Pending    bool   `json:"pending"`
	Revision   int64  `json:"revision"`
}

func natSummary(v store.ServerNATView) *ServerNATSummary {
	s := &ServerNATSummary{Source: "unknown", Status: "unknown", Editable: v.Editable, Reason: v.Reason, Pending: v.Pending, Revision: v.Revision}
	if v.Report != nil {
		s.Supported = true
		s.Source = v.Report.Source
		s.Configured = v.Report.Configured
		s.Status = v.Report.Status
		s.Error = v.Report.Error
	}
	if v.Pending {
		s.Status = "pending"
		s.Error = ""
	}
	return s
}

func natAPIError(err error) error {
	if errors.Is(err, store.ErrNATMappingRequired) {
		return httpx.Conflict(err.Error())
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return httpx.ErrNotFound
	case errors.Is(err, store.ErrNATRevision):
		return httpx.E(409, "nat_revision_conflict", err.Error())
	case errors.Is(err, store.ErrNATEdit), errors.Is(err, store.ErrNATNotReady):
		return httpx.E(409, "nat_not_ready", err.Error())
	case errors.Is(err, store.ErrNATResources):
		return httpx.E(409, "nat_resource_conflict", err.Error())
	case errors.Is(err, store.ErrNATPublicHost):
		return httpx.BadRequest(err.Error())
	default:
		return err
	}
}

func (a *API) serverNAT(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	v, err := a.Store.ServerNAT(r.Context(), id)
	if err != nil {
		return natAPIError(err)
	}
	httpx.OK(w, v)
	return nil
}

func (a *API) saveServerNAT(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in map[string]json.RawMessage
	if err = httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in) != 2 || in["config"] == nil || in["expected_revision"] == nil {
		return httpx.BadRequest("NAT 请求只接受 expected_revision 和 config")
	}
	var expected int64
	if err = json.Unmarshal(in["expected_revision"], &expected); err != nil || expected < 0 || expected > 1<<53-1 || string(in["expected_revision"]) == "null" {
		return httpx.BadRequest("NAT expected_revision 无效")
	}
	var cfg *natconfig.Config
	if string(in["config"]) != "null" {
		c, err := natconfig.Decode(in["config"])
		if err != nil {
			return httpx.BadRequest(err.Error())
		}
		cfg = &c
	}
	v, err := a.Store.SaveServerNAT(r.Context(), id, expected, cfg)
	if err != nil {
		return natAPIError(err)
	}
	// The durable dirty generation is retried by the existing publication job.
	// This response attests only to saving intent, never provider reachability.
	if _, _, err = a.Desired.Publish(r.Context(), id); err != nil {
		a.Logger.Warn("NAT configuration publication queued for retry", "server_id", id)
	}
	a.audit(r, "server.nat", r.PathValue("id"), map[string]any{"revision": v.Revision, "configured": cfg != nil && len(cfg.Mappings) > 0})
	httpx.OK(w, v)
	return nil
}
