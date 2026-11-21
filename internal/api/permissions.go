package api

import (
	"errors"
	"fmt"
	"net/http"

	"ctlvps/internal/auth"
	"ctlvps/internal/authorization"
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

type permissionView struct {
	authorization.Grant
	Effective bool `json:"effective"`
}

func permissionViews(e authorization.Evaluator, userID int64) []permissionView {
	out := []permissionView{}
	for _, g := range e.Grants {
		if g.UserID == userID {
			out = append(out, permissionView{Grant: g, Effective: e.Allows(userID, g.Permission, g.Scope)})
		}
	}
	return out
}

func (a *API) myPermissions(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r.Context())
	e, err := a.Store.PermissionSnapshot(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"full_admin": isAdmin(u), "grants": permissionViews(e, u.ID)})
	return nil
}

func (a *API) userPermissions(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r.Context())
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	e, err := a.Store.PermissionSnapshot(r.Context())
	if err != nil {
		return err
	}
	if id != u.ID && !e.Allows(u.ID, "accounts.view", authorization.Scope{Kind: "all"}) {
		return httpx.ErrForbidden
	}
	p, ok := e.Principals[id]
	if !ok {
		return httpx.ErrNotFound
	}
	httpx.OK(w, map[string]any{"full_admin": p.Admin, "grants": permissionViews(e, id)})
	return nil
}

type permissionGrantInput struct {
	Scope    authorization.Scope `json:"scope"`
	Password string              `json:"password"`
	Code     string              `json:"code"`
}

type permissionReauthInput struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (a *API) permissionReauth(w http.ResponseWriter, r *http.Request, password, code string) error {
	if len(password) > 512 || len(code) > 128 {
		return httpx.BadRequest("验证参数过长")
	}
	u := userFrom(r.Context())
	if !a.factorLimiter.AllowN(fmt.Sprintf("permission-edit:%d", u.ID), 5) {
		w.Header().Set("Retry-After", "60")
		return httpx.E(429, "rate_limited", "操作验证过于频繁，请一分钟后重试")
	}
	if !auth.VerifyPassword(u.PasswordHash, password) {
		return httpx.E(403, "invalid_password", "账户密码不正确")
	}
	if u.TOTPEnabled {
		before := *u
		if _, ok := a.consumeSecondFactor(u, code); !ok {
			return httpx.E(403, "invalid_code", "两步验证码不正确或已使用")
		}
		if err := a.Store.UpdateUserSecurity(r.Context(), before, *u); err != nil {
			return httpx.Conflict("验证状态已变化，请使用新的验证码")
		}
	}
	return nil
}

func (a *API) savePermissionGrant(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in permissionGrantInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	permission := r.PathValue("permission")
	if !authorization.ValidGrantScope(permission, in.Scope) || !implementedDelegation(permission) {
		return httpx.BadRequest("权限或资源范围无效")
	}
	if err := a.permissionReauth(w, r, in.Password, in.Code); err != nil {
		return err
	}
	g := authorization.Grant{UserID: id, Permission: permission, Scope: in.Scope}
	if err := a.Store.SavePermissionGrant(r.Context(), *userFrom(r.Context()), &g, a.auditIdentity(r)); err != nil {
		switch {
		case errors.Is(err, store.ErrPermissionDenied):
			return httpx.ErrForbidden
		case errors.Is(err, store.ErrPermissionCycle):
			return httpx.Conflict("授权关系不能形成循环")
		case errors.Is(err, store.ErrNotFound):
			return httpx.BadRequest("授权资源不存在")
		default:
			return err
		}
	}
	httpx.OK(w, g)
	return nil
}

type permissionBatchGrant struct {
	Permission string              `json:"permission"`
	Scope      authorization.Scope `json:"scope"`
}

type permissionBatchInput struct {
	Grants   []permissionBatchGrant `json:"grants"`
	Password string                 `json:"password"`
	Code     string                 `json:"code"`
}

func (a *API) replacePermissionGrants(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in permissionBatchInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Grants == nil {
		return httpx.BadRequest("必须提供 grants 数组")
	}
	grants := make([]authorization.Grant, 0, len(in.Grants))
	for _, input := range in.Grants {
		if !authorization.ValidGrantScope(input.Permission, input.Scope) || !implementedDelegation(input.Permission) {
			return httpx.BadRequest("权限或资源范围无效")
		}
		grants = append(grants, authorization.Grant{Permission: input.Permission, Scope: input.Scope})
	}
	if err := a.permissionReauth(w, r, in.Password, in.Code); err != nil {
		return err
	}
	grants, err = a.Store.ReplaceIssuedPermissionGrants(r.Context(), *userFrom(r.Context()), id, grants, a.auditIdentity(r))
	if err != nil {
		switch {
		case errors.Is(err, store.ErrPermissionDenied):
			return httpx.ErrForbidden
		case errors.Is(err, store.ErrPermissionCycle):
			return httpx.Conflict("授权关系不能形成循环")
		case errors.Is(err, store.ErrNotFound):
			return httpx.BadRequest("授权资源不存在")
		default:
			return err
		}
	}
	httpx.OK(w, map[string]any{"grants": grants})
	return nil
}

func (a *API) revokePermissionGrant(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in permissionReauthInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.permissionReauth(w, r, in.Password, in.Code); err != nil {
		return err
	}
	if err := a.Store.RevokePermissionGrant(r.Context(), *userFrom(r.Context()), id, a.auditIdentity(r)); err != nil {
		switch {
		case errors.Is(err, store.ErrPermissionDenied):
			return httpx.ErrForbidden
		case errors.Is(err, store.ErrNotFound):
			return httpx.ErrNotFound
		default:
			return err
		}
	}
	httpx.NoContent(w)
	return nil
}
