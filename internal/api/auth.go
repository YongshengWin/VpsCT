package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *API) setupStatus(w http.ResponseWriter, r *http.Request) error {
	n, err := a.Store.CountUsers(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"needs_setup": n == 0, "site_name": a.Store.GetSetting(r.Context(), domain.SettingSiteName, defaultSiteName)})
	return nil
}

func (a *API) setup(w http.ResponseWriter, r *http.Request) error {
	n, err := a.Store.CountUsers(r.Context())
	if err != nil {
		return err
	}
	if n > 0 {
		return httpx.Conflict("系统已初始化")
	}
	if !a.loginLimiter.Allow(httpx.ClientIP(r, a.Config.TrustProxy)) {
		return httpx.E(http.StatusTooManyRequests, "rate_limited", "尝试过于频繁，请稍后再试")
	}
	var c struct {
		credentials
		SetupToken string `json:"setup_token"`
	}
	if err := httpx.Decode(r, &c); err != nil {
		return err
	}
	if a.Config.SetupToken == "" || subtle.ConstantTimeCompare([]byte(auth.HashToken(strings.TrimSpace(c.SetupToken))), []byte(auth.HashToken(a.Config.SetupToken))) != 1 {
		return httpx.E(http.StatusForbidden, "invalid_setup_token", "初始化令牌不正确，请从安装服务器获取")
	}
	if err := validateCredentials(c.credentials); err != nil {
		return err
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		return err
	}
	u := &domain.User{Username: strings.TrimSpace(c.Username), PasswordHash: hash, Role: domain.RoleAdmin, Enabled: true}
	if err := a.Store.CreateInitialAdmin(r.Context(), u); err != nil {
		if errors.Is(err, store.ErrAlreadyInitialized) {
			return httpx.Conflict("系统已初始化")
		}
		return err
	}
	if a.Config.DataDir != "" {
		if err := os.Remove(filepath.Join(a.Config.DataDir, "setup-token")); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.Logger.Warn("remove spent setup token", "err", err)
		}
	}
	current, err := a.Store.GetUser(r.Context(), u.ID)
	if err != nil || !current.Enabled || current.PasswordHash != u.PasswordHash || current.TOTPEnabled != u.TOTPEnabled {
		return httpx.ErrUnauthorized
	}
	if err := a.startSession(w, r, u); err != nil {
		return err
	}
	a.Store.AddAudit(r.Context(), domain.AuditEvent{UserID: &u.ID, Username: u.Username, Action: "setup", Target: "system", IP: httpx.ClientIP(r, a.Config.TrustProxy)})
	httpx.OK(w, u)
	return nil
}

func validateCredentials(c credentials) error {
	name := strings.TrimSpace(c.Username)
	if len(name) < 2 || len(name) > 64 {
		return httpx.BadRequest("用户名长度需为 2-64 个字符")
	}
	if len(c.Password) < 8 || len(c.Password) > 512 {
		return httpx.BadRequest("密码至少 8 位")
	}
	return nil
}

func validateNickname(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.ContainsAny(s, "\n\r\t") {
		return "", httpx.BadRequest("昵称不能包含换行或制表符")
	}
	if utf8.RuneCountInString(s) > 32 {
		return "", httpx.BadRequest("昵称最多 32 个字符")
	}
	return s, nil
}

func (a *API) login(w http.ResponseWriter, r *http.Request) error {
	ip := httpx.ClientIP(r, a.Config.TrustProxy)
	if !a.loginLimiter.Allow(ip) {
		return httpx.E(http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请稍后再试")
	}
	var c credentials
	if err := httpx.Decode(r, &c); err != nil {
		return err
	}
	u, err := a.Store.GetUserByName(r.Context(), strings.TrimSpace(c.Username))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.Logger.Warn("login user lookup failed", "err", err)
		return httpx.E(http.StatusServiceUnavailable, "auth_unavailable", "身份验证暂时不可用，请稍后重试")
	}
	if err != nil || !auth.VerifyPassword(u.PasswordHash, c.Password) {
		return httpx.E(http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
	}
	if !u.Enabled {
		return httpx.E(http.StatusForbidden, "disabled", "账号已被禁用")
	}
	if u.TOTPEnabled {
		// password accepted; hand out a short-lived challenge for the second step
		httpx.OK(w, map[string]any{"requires_2fa": true, "challenge": a.challenges.issue(u.ID, auth.HashToken(u.PasswordHash+"|"+u.TOTPSecret))})
		return nil
	}
	return a.finishLogin(w, r, &u, ip, "login")
}

// finishLogin creates the session cookie after all factors are satisfied.
func (a *API) finishLogin(w http.ResponseWriter, r *http.Request, u *domain.User, ip, action string) error {
	current, err := a.Store.GetUser(r.Context(), u.ID)
	if err != nil || !current.Enabled || current.PasswordHash != u.PasswordHash || current.TOTPEnabled != u.TOTPEnabled {
		return httpx.ErrUnauthorized
	}
	if err := a.startSession(w, r, u); err != nil {
		return err
	}
	_ = a.Store.TouchLogin(r.Context(), u.ID)
	a.Store.AddAudit(r.Context(), domain.AuditEvent{UserID: &u.ID, Username: u.Username, Action: action, Target: "session", IP: ip})
	httpx.OK(w, u)
	return nil
}

// login2FA completes a login that returned requires_2fa. The code may be a
// TOTP code or one of the user's recovery codes.
func (a *API) login2FA(w http.ResponseWriter, r *http.Request) error {
	ip := httpx.ClientIP(r, a.Config.TrustProxy)
	// the challenge itself burns after a few wrong codes; this only slows down
	// someone hammering the endpoint with fresh challenges
	if !a.factorLimiter.Allow(ip) {
		return httpx.E(http.StatusTooManyRequests, "rate_limited", "尝试过于频繁，请稍后再试")
	}
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	challenge, ok := a.challenges.attempt(in.Challenge)
	if !ok {
		return httpx.E(http.StatusUnauthorized, "challenge_expired", "验证已过期，请重新登录")
	}
	u, err := a.Store.GetUser(r.Context(), challenge.userID)
	if err != nil || !u.Enabled || !u.TOTPEnabled || challenge.credentials != auth.HashToken(u.PasswordHash+"|"+u.TOTPSecret) {
		return httpx.E(http.StatusUnauthorized, "challenge_expired", "验证已过期，请重新登录")
	}
	before := u
	used, ok := a.consumeSecondFactor(&u, in.Code)
	if !ok {
		return httpx.E(http.StatusUnauthorized, "invalid_code", "验证码不正确")
	}
	if !a.challenges.drop(in.Challenge) {
		return httpx.ErrUnauthorized
	}
	if err := a.Store.UpdateUserSecurity(r.Context(), before, u); err != nil {
		return httpx.E(401, "invalid_code", "验证码已使用或验证状态变化")
	}
	action := "login"
	if used == "recovery" {
		action = "login.recovery_code"
	}
	return a.finishLogin(w, r, &u, ip, action)
}

// consumeSecondFactor validates a TOTP or recovery code and updates the
// user's anti-replay state / remaining recovery codes in memory (caller saves).
func (a *API) consumeSecondFactor(u *domain.User, code string) (kind string, ok bool) {
	code = strings.TrimSpace(code)
	if step, ok := auth.VerifyTOTP(u.TOTPSecret, code, a.Store.Now(), u.TOTPLastStep); ok {
		u.TOTPLastStep = step
		return "totp", true
	}
	if rest, ok := auth.UseRecoveryCode(u.RecoveryCodes, code); ok {
		u.RecoveryCodes = rest
		return "recovery", true
	}
	return "", false
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request, u *domain.User) error {
	token := auth.RandomToken(32)
	now := a.Store.Now()
	sess := store.Session{ID: auth.HashToken(token), UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(a.Config.SessionTTL), UserAgent: r.UserAgent(), IP: httpx.ClientIP(r, a.Config.TrustProxy)}
	if err := a.Store.CreateAuthorizedSession(r.Context(), sess, *u); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     a.sessionName(),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.Config.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.Config.SessionTTL.Seconds()),
	})
	return nil
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) error {
	if _, err := r.Cookie(a.sessionName()); err == nil && !a.checkCSRF(r) {
		return httpx.ErrForbidden
	}
	if c, err := r.Cookie(a.sessionName()); err == nil && c.Value != "" {
		_ = a.Store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: a.sessionName(), Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: a.Config.SecureCookies})
	httpx.NoContent(w)
	return nil
}

func (a *API) me(w http.ResponseWriter, r *http.Request) error {
	httpx.OK(w, userFrom(r.Context()))
	return nil
}

func (a *API) setMyProfile(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Nickname *string `json:"nickname"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u := userFrom(r.Context())
	if in.Nickname != nil {
		n, err := validateNickname(*in.Nickname)
		if err != nil {
			return err
		}
		u.Nickname = n
	}
	if err := a.Store.UpdateUser(r.Context(), u); err != nil {
		return err
	}
	httpx.OK(w, u)
	return nil
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u := userFrom(r.Context())
	if !auth.VerifyPassword(u.PasswordHash, in.Old) {
		return httpx.BadRequest("旧密码不正确")
	}
	if len(in.New) < 8 || len(in.New) > 512 {
		return httpx.BadRequest("新密码至少 8 位")
	}
	hash, err := auth.HashPassword(in.New)
	if err != nil {
		return err
	}
	u.PasswordHash = hash
	if err := a.Store.UpdateUser(r.Context(), u); err != nil {
		return err
	}
	// invalidate other sessions, keep this one
	if c, err := r.Cookie(a.sessionName()); err == nil {
		_ = a.Store.DeleteUserSessions(r.Context(), u.ID)
		_ = a.Store.CreateSession(r.Context(), store.Session{ID: auth.HashToken(c.Value), UserID: u.ID, CreatedAt: a.Store.Now(), ExpiresAt: a.Store.Now().Add(a.Config.SessionTTL)})
	}
	a.audit(r, "password.change", "self", nil)
	httpx.NoContent(w)
	return nil
}

// ---- users ----

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) error {
	users, err := a.Store.ListUsers(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, users)
	return nil
}

type userInput struct {
	Username string      `json:"username"`
	Nickname *string     `json:"nickname"`
	Password string      `json:"password"`
	Role     domain.Role `json:"role"`
	Enabled  *bool       `json:"enabled"`
	Avatar   *string     `json:"avatar"` // "", preset:<id> or image data URL
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) error {
	var in userInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := validateCredentials(credentials{Username: in.Username, Password: in.Password}); err != nil {
		return err
	}
	if in.Role != domain.RoleAdmin {
		in.Role = domain.RoleUser
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return err
	}
	u := &domain.User{Username: strings.TrimSpace(in.Username), PasswordHash: hash, Role: in.Role, Enabled: true}
	if in.Nickname != nil {
		n, err := validateNickname(*in.Nickname)
		if err != nil {
			return err
		}
		u.Nickname = n
	}
	if in.Enabled != nil {
		u.Enabled = *in.Enabled
	}
	if err := a.Store.CreateUser(r.Context(), u); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return httpx.Conflict("用户名已存在")
		}
		return err
	}
	a.audit(r, "user.create", u.Username, map[string]any{"role": u.Role})
	httpx.JSON(w, http.StatusCreated, u)
	return nil
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	u, err := a.Store.GetUser(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	var in userInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	me := userFrom(r.Context())
	if in.Username != "" {
		u.Username = strings.TrimSpace(in.Username)
	}
	if in.Nickname != nil {
		n, err := validateNickname(*in.Nickname)
		if err != nil {
			return err
		}
		u.Nickname = n
	}
	if in.Role != "" && in.Role != domain.RoleAdmin && in.Role != domain.RoleUser {
		return httpx.BadRequest("角色无效")
	}
	if in.Role != "" {
		if u.ID == me.ID && in.Role != domain.RoleAdmin {
			return httpx.BadRequest("不能降级自己的角色")
		}
		u.Role = in.Role
	}
	if in.Enabled != nil {
		if u.ID == me.ID && !*in.Enabled {
			return httpx.BadRequest("不能禁用自己")
		}
		u.Enabled = *in.Enabled
	}
	if in.Avatar != nil {
		normalized, err := normalizeAvatar(*in.Avatar)
		if err != nil {
			return err
		}
		u.Avatar = normalized
	}
	if in.Password != "" {
		if len(in.Password) < 8 || len(in.Password) > 512 {
			return httpx.BadRequest("密码至少 8 位")
		}
		hash, err := auth.HashPassword(in.Password)
		if err != nil {
			return err
		}
		u.PasswordHash = hash
		_ = a.Store.DeleteUserSessions(r.Context(), u.ID)
	}
	if err := a.Store.UpdateUser(r.Context(), &u); err != nil {
		return err
	}
	if in.Role != "" || in.Enabled != nil {
		_ = a.Store.DeleteUserSessions(r.Context(), u.ID)
	}
	a.audit(r, "user.update", u.Username, map[string]any{"role": u.Role, "enabled": u.Enabled, "password_changed": in.Password != ""})
	httpx.OK(w, u)
	return nil
}

func (a *API) deleteUser(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	if id == userFrom(r.Context()).ID {
		return httpx.BadRequest("不能删除自己")
	}
	u, err := a.Store.GetUser(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := a.Store.DeleteUser(r.Context(), id); err != nil {
		return err
	}
	a.audit(r, "user.delete", u.Username, nil)
	httpx.NoContent(w)
	return nil
}
