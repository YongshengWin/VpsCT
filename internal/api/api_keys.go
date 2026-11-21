package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ctlvps/internal/auth"
	"ctlvps/internal/authorization"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

const apiKeyContextKey ctxKey = 2
const apiKeyPrefix = "vpsct_"

func apiKeyFrom(ctx context.Context) *domain.APIKey {
	k, _ := ctx.Value(apiKeyContextKey).(*domain.APIKey)
	return k
}

// A supplied Authorization header is authoritative. Invalid keys must never
// fall back to a browser cookie that happens to accompany the request.
func (a *API) apiKeyIdentity(r *http.Request) (*domain.APIKey, *domain.User, error) {
	invalid := httpx.E(401, "invalid_api_key", "API key 无效、已过期或已撤销")
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return nil, nil, invalid
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], apiKeyPrefix) {
		return nil, nil, invalid
	}
	raw := strings.TrimPrefix(parts[1], apiKeyPrefix)
	if len(raw) != 43 {
		return nil, nil, invalid
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return nil, nil, invalid
	}
	k, err := a.Store.GetAPIKeyByHash(r.Context(), auth.HashToken(parts[1]))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, invalid
		}
		a.Logger.Warn("API key lookup failed", "err", err)
		return nil, nil, httpx.E(503, "auth_unavailable", "身份验证暂时不可用")
	}
	u, err := a.Store.GetUser(r.Context(), k.UserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, httpx.E(503, "auth_unavailable", "身份验证暂时不可用")
	}
	if err != nil || !u.Enabled || u.SecurityVersion != k.SecurityVersion {
		return nil, nil, invalid
	}
	if u.Role != domain.RoleAdmin {
		e, err := a.Store.PermissionSnapshot(r.Context())
		if err != nil {
			return nil, nil, httpx.E(503, "auth_unavailable", "身份验证暂时不可用")
		}
		if !e.Allows(u.ID, "ai.access", authorization.Scope{Kind: "all"}) {
			return nil, nil, invalid
		}
	}
	return &k, &u, nil
}

// Discovery remains available for every key so a restricted agent can inspect
// its current limits. Other operations require an explicit allowlist match.
func agentOperationID(pattern string) string {
	method, path, _ := strings.Cut(pattern, " ")
	return strings.ToLower(method) + "_" + strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_", ".", "_").Replace(strings.TrimPrefix(path, "/api/v1/"))
}

// Ordinary-user AI access follows the share/detail product boundary. Keep this
// list explicit: adding a member-access route does not silently expose it to AI.
func (a *API) memberAIOperationAllowed(ctx context.Context, u *domain.User, pattern string) bool {
	if pattern == "GET /api/v1/templates/{id}/versions" {
		e, err := a.Store.PermissionSnapshot(ctx)
		return err == nil && (e.Allows(u.ID, "templates.create", authorization.Scope{Kind: "all"}) || e.Allows(u.ID, "templates.edit", authorization.Scope{Kind: "all"}) || e.Allows(u.ID, "templates.delete", authorization.Scope{Kind: "all"}))
	}
	if delegatedGlobalRoutes[pattern] != "" || delegatedShareRoutes[pattern] != "" || pattern == "POST /api/v1/shares" {
		return true
	}
	switch pattern {
	case "GET /api/v1/shares", "GET /api/v1/shares/{id}", "GET /api/v1/share-pages/{public_id}",
		"GET /api/v1/shares/{id}/events", "GET /api/v1/shares/{id}/traffic",
		"GET /api/v1/shares/{id}/personal-templates", "POST /api/v1/shares/{id}/personal-templates/preview",
		"POST /api/v1/shares/{id}/personal-templates/preview-draft", "PUT /api/v1/shares/{id}/personal-templates/{format}",
		"DELETE /api/v1/shares/{id}/personal-templates/{format}",
		"GET /api/v1/me/templates", "POST /api/v1/me/templates", "GET /api/v1/me/templates/{id}",
		"GET /api/v1/me/templates/{id}/versions", "PUT /api/v1/me/templates/{id}", "DELETE /api/v1/me/templates/{id}",
		"GET /api/v1/templates", "GET /api/v1/events":
		return true
	}
	return false
}

func apiKeyAllowsOperation(k *domain.APIKey, pattern string) bool {
	if k == nil || k.Operations == nil {
		return true
	}
	switch pattern {
	case "GET /api/v1/agent/capabilities", "GET /api/v1/agent/openapi.json", "GET /api/v1/agent/skill", "GET /api/v1/agent/skill.zip", "GET /api/v1/agent/client.py", "GET /api/v1/agent/reference":
		return true
	}
	return slices.Contains(k.Operations, agentOperationID(pattern))
}

func (a *API) validateKeyOperations(r *http.Request, operations []string) error {
	if operations == nil {
		return nil
	}
	if len(operations) > 512 {
		return httpx.BadRequest("选择的功能过多")
	}
	allowed := map[string]bool{}
	for _, op := range a.visibleAgentOperations(r) {
		if op.Available && !op.SessionOnly && !strings.HasPrefix(op.Path, "/api/v1/agent/") {
			allowed[op.OperationID] = true
		}
	}
	seen := map[string]bool{}
	for _, id := range operations {
		if !allowed[id] || seen[id] {
			return httpx.BadRequest("功能无效、重复或超出当前账户权限")
		}
		seen[id] = true
	}
	return nil
}

func hasAPIKeyScope(k *domain.APIKey, scope string) bool {
	return slices.Contains(k.Scopes, scope)
}

func operationScope(pattern string) string {
	if strings.HasPrefix(pattern, "GET ") || strings.HasPrefix(pattern, "HEAD ") {
		return "read"
	}
	return "write"
}

func sessionOnlyOperation(pattern string) bool {
	_, path, _ := strings.Cut(pattern, " ")
	return path == "/api/v1/auth/csrf" || strings.HasPrefix(path, "/api/v1/auth/2fa/") ||
		path == "/api/v1/api-keys" || strings.HasPrefix(path, "/api/v1/api-keys/") ||
		strings.HasSuffix(path, "/2fa/reset")
}

func (a *API) listAPIKeys(w http.ResponseWriter, r *http.Request) error {
	keys, err := a.Store.ListAPIKeys(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	if keys == nil {
		keys = []domain.APIKey{}
	}
	httpx.OK(w, map[string]any{"list": keys})
	return nil
}

func (a *API) createAPIKey(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name       string     `json:"name"`
		Scopes     []string   `json:"scopes"`
		Operations []string   `json:"operations"`
		ExpiresAt  *time.Time `json:"expires_at"`
		Password   string     `json:"password"`
		Code       string     `json:"code"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 80 || strings.ContainsFunc(in.Name, unicode.IsControl) {
		return httpx.BadRequest("请输入不超过 80 字的 API key 名称")
	}
	if len(in.Scopes) == 0 || len(in.Scopes) > 2 {
		return httpx.BadRequest("请选择 read 或 read、write 权限")
	}
	slices.Sort(in.Scopes)
	for i, scope := range in.Scopes {
		if (scope != "read" && scope != "write") || (i > 0 && in.Scopes[i-1] == scope) {
			return httpx.BadRequest("API key 权限无效")
		}
	}
	if err := a.validateKeyOperations(r, in.Operations); err != nil {
		return err
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(a.Store.Now()) {
		return httpx.BadRequest("有效期必须晚于当前时间")
	}
	if len(in.Password) > 512 || len(in.Code) > 128 {
		return httpx.BadRequest("验证参数过长")
	}
	u := userFrom(r.Context())
	if !a.factorLimiter.AllowN(fmt.Sprintf("api-key-create:%d", u.ID), 5) {
		w.Header().Set("Retry-After", "60")
		return httpx.E(429, "rate_limited", "操作验证过于频繁，请一分钟后重试")
	}
	if !auth.VerifyPassword(u.PasswordHash, in.Password) {
		return httpx.E(403, "invalid_password", "账户密码不正确")
	}
	if u.TOTPEnabled {
		before := *u
		if _, ok := a.consumeSecondFactor(u, in.Code); !ok {
			return httpx.E(403, "invalid_code", "两步验证码不正确或已使用")
		}
		if err := a.Store.UpdateUserSecurity(r.Context(), before, *u); err != nil {
			return httpx.Conflict("验证状态已变化，请使用新的验证码")
		}
	}
	plain := apiKeyPrefix + auth.RandomToken(32)
	k := domain.APIKey{UserID: u.ID, SecurityVersion: u.SecurityVersion, Name: in.Name, Scopes: in.Scopes,
		TokenHash: auth.HashToken(plain), TokenHint: plain[:12], ExpiresAt: in.ExpiresAt, Operations: in.Operations}
	// Persist the intent without credentials before issuing a new capability.
	if err := a.audit(r, "api_key.create.request", "self", map[string]any{"name": k.Name, "scopes": k.Scopes, "operations": k.Operations, "expires_at": k.ExpiresAt}); err != nil {
		return httpx.E(503, "audit_unavailable", "无法持久化安全审计，未创建 API key")
	}
	if err := a.Store.CreateAPIKey(r.Context(), &k); err != nil {
		switch {
		case errors.Is(err, store.ErrAPIKeyLimit):
			return httpx.Conflict("最多保留 32 个有效 API key，请先撤销不用的 key")
		case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrNotFound):
			return httpx.Conflict("账户验证状态已变化，请重新登录后再试")
		default:
			return err
		}
	}
	a.audit(r, "api_key.create", fmt.Sprintf("api_key:%d", k.ID), map[string]any{"scopes": k.Scopes})
	httpx.JSON(w, 201, map[string]any{"api_key": k, "key": plain})
	return nil
}

func (a *API) revokeAPIKey(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if err := a.Store.RevokeAPIKey(r.Context(), userFrom(r.Context()).ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		}
		return err
	}
	a.audit(r, "api_key.revoke", fmt.Sprintf("api_key:%d", id), nil)
	httpx.NoContent(w)
	return nil
}

func (a *API) updateAPIKeyOperations(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Operations json.RawMessage `json:"operations"`
		Password   string          `json:"password"`
		Code       string          `json:"code"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if len(in.Operations) == 0 {
		return httpx.BadRequest("必须提供 operations")
	}
	var operations []string
	if err := json.Unmarshal(in.Operations, &operations); err != nil {
		return httpx.BadRequest("功能列表无效")
	}
	if err := a.validateKeyOperations(r, operations); err != nil {
		return err
	}
	if err := a.permissionReauth(w, r, in.Password, in.Code); err != nil {
		return err
	}
	if err := a.Store.UpdateAPIKeyOperations(r.Context(), *userFrom(r.Context()), id, operations, a.auditIdentity(r)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound
		}
		return err
	}
	httpx.OK(w, map[string]any{"operations": operations})
	return nil
}
