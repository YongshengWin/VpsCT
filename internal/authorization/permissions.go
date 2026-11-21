// Package authorization evaluates explicit, delegated product permissions.
// It contains no implicit route permissions; callers must map every operation
// and resource to an appropriate permission and fail closed when unmapped.
package authorization

import (
	"errors"
	"slices"
	"strings"
)

const Delegate = "accounts.delegate"

// Permissions are explicit product operations. Adding a name here does not
// expose an API route: each route must also opt into enforcement.
var permissions = map[string]bool{
	"servers.view": true, "servers.create": true, "servers.edit": true, "servers.delete": true, "servers.maintenance": true,
	"nodes.view": true, "nodes.create": true, "nodes.deploy": true, "nodes.edit": true, "nodes.stop": true, "nodes.delete": true,
	"shares.view": true, "shares.links": true, "shares.create": true, "shares.edit": true, "shares.pause": true, "shares.revoke": true, "shares.reissue": true, "shares.reset": true, "shares.delete": true,
	"subscriptions.view": true, "subscriptions.create": true, "subscriptions.edit": true, "subscriptions.delete": true,
	"templates.view": true, "templates.create": true, "templates.edit": true, "templates.delete": true, "templates.apply": true,
	"traffic.view": true, "connlog.view": true, "connlog.delete": true,
	"accounts.view": true, "accounts.create": true, "accounts.edit": true, "accounts.disable": true, "accounts.delete": true, Delegate: true,
	"ai.access":     true,
	"settings.view": true, "settings.edit": true, "settings.maintenance": true, "audit.view": true,
}

func ValidPermission(permission string) bool { return permissions[permission] }

// ValidGrantScope limits scopes to resource types that can be checked for
// the operation. Unsupported combinations never silently become global.
func ValidGrantScope(permission string, scope Scope) bool {
	if !ValidPermission(permission) {
		return false
	}
	s, err := ValidateScope(scope)
	if err != nil {
		return false
	}
	if s.Kind == "all" {
		return true
	}
	if s.Kind == "servers" {
		return (strings.HasPrefix(permission, "servers.") && permission != "servers.create") || strings.HasPrefix(permission, "nodes.") || strings.HasPrefix(permission, "shares.") || strings.HasPrefix(permission, "traffic.") || strings.HasPrefix(permission, "connlog.")
	}
	return s.Kind == "shares" && (strings.HasPrefix(permission, "shares.") && permission != "shares.create" || permission == "traffic.view" || strings.HasPrefix(permission, "connlog."))
}

type Scope struct {
	Kind string  `json:"kind"` // all, servers, shares
	IDs  []int64 `json:"ids,omitempty"`
}

type Grant struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	GrantedBy  int64  `json:"granted_by"`
	Permission string `json:"permission"`
	Scope      Scope  `json:"scope"`
}

type Principal struct {
	ID      int64
	Enabled bool
	Admin   bool
}

// ValidateScope canonicalizes resource IDs so comparison and storage have a
// stable representation. Resource types never implicitly grant each other.
func ValidateScope(s Scope) (Scope, error) {
	switch s.Kind {
	case "all":
		if len(s.IDs) != 0 {
			return Scope{}, errors.New("all scope cannot include resource IDs")
		}
		return Scope{Kind: "all"}, nil
	case "servers", "shares":
		if len(s.IDs) == 0 || len(s.IDs) > 256 {
			return Scope{}, errors.New("resource scope must contain 1 to 256 IDs")
		}
		ids := slices.Clone(s.IDs)
		slices.Sort(ids)
		for i, id := range ids {
			if id <= 0 || i > 0 && ids[i-1] == id {
				return Scope{}, errors.New("invalid or duplicate resource ID")
			}
		}
		return Scope{Kind: s.Kind, IDs: ids}, nil
	default:
		return Scope{}, errors.New("unknown resource scope")
	}
}

func (s Scope) Covers(other Scope) bool {
	parent, err := ValidateScope(s)
	if err != nil {
		return false
	}
	child, err := ValidateScope(other)
	if err != nil {
		return false
	}
	if parent.Kind == "all" {
		return true
	}
	if parent.Kind != child.Kind {
		return false
	}
	for _, id := range child.IDs {
		if !slices.Contains(parent.IDs, id) {
			return false
		}
	}
	return true
}

// Evaluator operates on one consistent snapshot of users and grants. Each
// delegation remains effective only while its grantor is enabled, may
// delegate, and retains an equal or broader grant for the same operation.
// Visited grant IDs prevent cycles from becoming an authorization source.
type Evaluator struct {
	Principals map[int64]Principal
	Grants     []Grant
}

func (e Evaluator) Allows(userID int64, permission string, wanted Scope) bool {
	if strings.TrimSpace(permission) == "" {
		return false
	}
	if _, err := ValidateScope(wanted); err != nil {
		return false
	}
	budget := 4096
	return e.allows(userID, permission, wanted, map[int64]bool{}, &budget)
}

func (e Evaluator) allows(userID int64, permission string, wanted Scope, visiting map[int64]bool, budget *int) bool {
	if *budget <= 0 || len(visiting) >= 64 {
		return false
	}
	*budget--
	p, ok := e.Principals[userID]
	if !ok || !p.Enabled {
		return false
	}
	if p.Admin {
		return true
	}
	for _, g := range e.Grants {
		if g.ID <= 0 || g.UserID != userID || g.GrantedBy == userID || g.Permission != permission || !g.Scope.Covers(wanted) || visiting[g.ID] {
			continue
		}
		visiting[g.ID] = true
		valid := e.allows(g.GrantedBy, permission, g.Scope, visiting, budget) &&
			e.allows(g.GrantedBy, Delegate, Scope{Kind: "all"}, visiting, budget) &&
			e.allows(g.GrantedBy, "accounts.edit", Scope{Kind: "all"}, visiting, budget)
		delete(visiting, g.ID)
		if valid {
			return true
		}
	}
	return false
}
