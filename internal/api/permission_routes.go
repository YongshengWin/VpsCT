package api

import (
	"context"
	"net/http"

	"ctlvps/internal/authorization"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
)

// Only explicitly reviewed routes may leave the legacy administrator boundary.
// Unlisted operations remain administrator-only until their resource checks
// and UI have been individually audited.
var delegatedGlobalRoutes = map[string]string{
	"GET /api/v1/api-keys":                 "ai.access",
	"POST /api/v1/api-keys":                "ai.access",
	"PUT /api/v1/api-keys/{id}/operations": "ai.access",
	"DELETE /api/v1/api-keys/{id}":         "ai.access",
	"GET /api/v1/agent/capabilities":       "ai.access",
	"GET /api/v1/agent/openapi.json":       "ai.access",
	"GET /api/v1/agent/skill":              "ai.access",
	"GET /api/v1/agent/skill.zip":          "ai.access",
	"GET /api/v1/agent/client.py":          "ai.access",
	"GET /api/v1/agent/reference":          "ai.access",
	"POST /api/v1/templates":               "templates.create",
	"PUT /api/v1/templates/{id}":           "templates.edit",
	"DELETE /api/v1/templates/{id}":        "templates.delete",
}

var delegatedShareRoutes = map[string]string{
	"PUT /api/v1/shares/{id}":          "shares.edit",
	"PUT /api/v1/shares/{id}/connlog":  "shares.edit",
	"DELETE /api/v1/shares/{id}":       "shares.delete",
	"POST /api/v1/shares/{id}/pause":   "shares.pause",
	"POST /api/v1/shares/{id}/resume":  "shares.pause",
	"POST /api/v1/shares/{id}/reset":   "shares.reset",
	"POST /api/v1/shares/{id}/revoke":  "shares.revoke",
	"POST /api/v1/shares/{id}/reissue": "shares.reissue",
}

func implementedDelegation(permission string) bool {
	if permission == "accounts.edit" || permission == authorization.Delegate || permission == "shares.view" || permission == "shares.links" || permission == "shares.create" || permission == "shares.pause" || permission == "shares.reset" || permission == "shares.revoke" || permission == "shares.reissue" || permission == "shares.delete" {
		return true
	}
	for _, available := range delegatedGlobalRoutes {
		if available == permission {
			return true
		}
	}
	return false
}

func (a *API) delegatedCreateAllowed(ctx context.Context, u *domain.User, permission string) bool {
	if u == nil {
		return false
	}
	e, err := a.Store.PermissionSnapshot(ctx)
	if err != nil {
		return false
	}
	for _, g := range e.Grants {
		if g.UserID == u.ID && g.Permission == permission && e.Allows(u.ID, permission, g.Scope) {
			return true
		}
	}
	return false
}

func hasAnyPermissionGrant(e authorization.Evaluator, userID int64, permission string) bool {
	for _, g := range e.Grants {
		if g.UserID == userID && g.Permission == permission && e.Allows(userID, permission, g.Scope) {
			return true
		}
	}
	return false
}

// A server-scoped share grant covers a share only when every deployed target
// and every extra node belongs to an explicitly authorized server. Empty and
// detached shares require an explicit share or global grant.
func (a *API) delegatedShareAllowed(ctx context.Context, u *domain.User, sh domain.Share, permission string) bool {
	if u == nil {
		return false
	}
	e, err := a.Store.PermissionSnapshot(ctx)
	if err != nil {
		return false
	}
	if e.Allows(u.ID, permission, authorization.Scope{Kind: "all"}) || e.Allows(u.ID, permission, authorization.Scope{Kind: "shares", IDs: []int64{sh.ID}}) {
		return true
	}
	ids := map[int64]bool{}
	for _, target := range sh.Targets {
		if target.ServerID <= 0 {
			return false
		}
		ids[target.ServerID] = true
	}
	for _, id := range sh.ExtraNodeIDs {
		node, err := a.Store.GetNode(ctx, id)
		if err != nil || node.ServerID == nil || *node.ServerID <= 0 {
			return false
		}
		ids[*node.ServerID] = true
	}
	if len(ids) == 0 {
		return false
	}
	for id := range ids {
		if !e.Allows(u.ID, permission, authorization.Scope{Kind: "servers", IDs: []int64{id}}) {
			return false
		}
	}
	return true
}

func (a *API) delegatedGlobalAllowed(ctx context.Context, u *domain.User, pattern string) (bool, error) {
	permission := delegatedGlobalRoutes[pattern]
	if permission == "" || u == nil {
		return false, nil
	}
	e, err := a.Store.PermissionSnapshot(ctx)
	if err != nil {
		return false, err
	}
	return e.Allows(u.ID, permission, authorization.Scope{Kind: "all"}), nil
}

func (a *API) delegatedShareRouteAllowed(r *http.Request, u *domain.User, pattern string) (bool, error) {
	permission := delegatedShareRoutes[pattern]
	if permission == "" || u == nil {
		return false, nil
	}
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return false, nil
	}
	sh, err := a.Store.GetShare(r.Context(), id)
	if err != nil {
		return false, err
	}
	return a.delegatedShareAllowed(r.Context(), u, sh, permission), nil
}
