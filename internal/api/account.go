package api

import (
	"encoding/base64"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
)

// ---- login challenges (second factor pending) ----

const (
	challengeTTL      = 5 * time.Minute
	challengeAttempts = 5
)

type challenge struct {
	userID   int64
	expires  time.Time
	attempts int
}

// challengeStore keeps password-verified logins that still need a TOTP code.
// It is in-memory on purpose: challenges are tiny, short-lived and there is a
// single ctlvpsd process.
type challengeStore struct {
	mu sync.Mutex
	m  map[string]*challenge
}

func newChallengeStore() *challengeStore { return &challengeStore{m: map[string]*challenge{}} }

func (s *challengeStore) issue(userID int64) string {
	tok := auth.RandomToken(24)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.m {
		if now.After(c.expires) {
			delete(s.m, k)
		}
	}
	s.m[tok] = &challenge{userID: userID, expires: now.Add(challengeTTL)}
	return tok
}

// attempt returns the user for a live challenge and counts the attempt;
// too many wrong codes burn the challenge.
func (s *challengeStore) attempt(tok string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[tok]
	if !ok || time.Now().After(c.expires) {
		delete(s.m, tok)
		return 0, false
	}
	c.attempts++
	if c.attempts > challengeAttempts {
		delete(s.m, tok)
		return 0, false
	}
	return c.userID, true
}

func (s *challengeStore) drop(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

// ---- two-factor management (self) ----

func (a *API) issuer(r *http.Request) string {
	return a.Store.GetSetting(r.Context(), domain.SettingSiteName, defaultSiteName)
}

type passwordCode struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// twoFASetup starts enrolment: stores a pending secret (enabled=false) and
// returns it with the otpauth URL for the QR code. Re-running replaces the
// pending secret; an already enabled account must disable first.
func (a *API) twoFASetup(w http.ResponseWriter, r *http.Request) error {
	var in passwordCode
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u := userFrom(r.Context())
	if !auth.VerifyPassword(u.PasswordHash, in.Password) {
		return httpx.BadRequest("密码不正确")
	}
	if u.TOTPEnabled {
		return httpx.Conflict("两步验证已开启，如需更换请先关闭")
	}
	u.TOTPSecret = auth.NewTOTPSecret()
	u.TOTPLastStep = 0
	if err := a.Store.UpdateUser(r.Context(), u); err != nil {
		return err
	}
	httpx.OK(w, map[string]any{
		"secret":      u.TOTPSecret,
		"otpauth_url": auth.OTPAuthURL(a.issuer(r), u.Username, u.TOTPSecret),
	})
	return nil
}

// twoFAEnable confirms the pending secret with a live code and hands out the
// recovery codes (shown exactly once).
func (a *API) twoFAEnable(w http.ResponseWriter, r *http.Request) error {
	var in passwordCode
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u := userFrom(r.Context())
	if u.TOTPEnabled {
		return httpx.Conflict("两步验证已开启")
	}
	if u.TOTPSecret == "" {
		return httpx.BadRequest("请先生成密钥")
	}
	step, ok := auth.VerifyTOTP(u.TOTPSecret, in.Code, a.Store.Now(), 0)
	if !ok {
		return httpx.BadRequest("验证码不正确，请确认手机时间准确后重试")
	}
	plain, hashes := auth.NewRecoveryCodes(8)
	u.TOTPEnabled = true
	u.TOTPLastStep = step
	u.RecoveryCodes = hashes
	if err := a.Store.UpdateUser(r.Context(), u); err != nil {
		return err
	}
	a.audit(r, "2fa.enable", "self", nil)
	httpx.OK(w, map[string]any{"recovery_codes": plain})
	return nil
}

// twoFADisable turns 2FA off; needs the password and a current code (TOTP or
// recovery) so a hijacked session alone cannot weaken the account.
func (a *API) twoFADisable(w http.ResponseWriter, r *http.Request) error {
	var in passwordCode
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u := userFrom(r.Context())
	if !auth.VerifyPassword(u.PasswordHash, in.Password) {
		return httpx.BadRequest("密码不正确")
	}
	if u.TOTPEnabled {
		if _, ok := a.consumeSecondFactor(u, in.Code); !ok {
			return httpx.BadRequest("验证码不正确")
		}
	}
	clearTOTP(u)
	if err := a.Store.UpdateUser(r.Context(), u); err != nil {
		return err
	}
	a.audit(r, "2fa.disable", "self", nil)
	httpx.NoContent(w)
	return nil
}

// twoFARecovery regenerates the recovery codes (old ones stop working).
func (a *API) twoFARecovery(w http.ResponseWriter, r *http.Request) error {
	var in passwordCode
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u := userFrom(r.Context())
	if !u.TOTPEnabled {
		return httpx.BadRequest("两步验证未开启")
	}
	if !auth.VerifyPassword(u.PasswordHash, in.Password) {
		return httpx.BadRequest("密码不正确")
	}
	if _, ok := a.consumeSecondFactor(u, in.Code); !ok {
		return httpx.BadRequest("验证码不正确")
	}
	plain, hashes := auth.NewRecoveryCodes(8)
	u.RecoveryCodes = hashes
	if err := a.Store.UpdateUser(r.Context(), u); err != nil {
		return err
	}
	a.audit(r, "2fa.recovery_codes", "self", nil)
	httpx.OK(w, map[string]any{"recovery_codes": plain})
	return nil
}

func clearTOTP(u *domain.User) {
	u.TOTPEnabled = false
	u.TOTPSecret = ""
	u.TOTPLastStep = 0
	u.RecoveryCodes = nil
}

// twoFAReset lets an admin switch 2FA off for a locked-out user.
func (a *API) twoFAReset(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	u, err := a.Store.GetUser(r.Context(), id)
	if err != nil {
		return httpx.ErrNotFound
	}
	clearTOTP(&u)
	if err := a.Store.UpdateUser(r.Context(), &u); err != nil {
		return err
	}
	_ = a.Store.DeleteUserSessions(r.Context(), u.ID)
	a.audit(r, "2fa.reset", u.Username, nil)
	httpx.OK(w, u)
	return nil
}

// ---- avatars ----

const (
	avatarMaxBytes = 256 << 10 // decoded image size; the client downsizes to 256px first
)

var (
	presetRe  = regexp.MustCompile(`^preset:[a-z0-9-]{1,32}$`)
	dataURLRe = regexp.MustCompile(`^data:(image/(?:png|jpeg|webp|gif));base64,([A-Za-z0-9+/=]+)$`)
)

// validateAvatar accepts "", a preset id or a bounded base64 image data URL.
func validateAvatar(v string) error {
	switch {
	case v == "", presetRe.MatchString(v):
		return nil
	case strings.HasPrefix(v, "data:"):
		m := dataURLRe.FindStringSubmatch(v)
		if m == nil {
			return httpx.BadRequest("头像只支持 PNG / JPEG / WebP / GIF 图片")
		}
		if base64.StdEncoding.DecodedLen(len(m[2])) > avatarMaxBytes {
			return httpx.BadRequest("头像图片过大（上限 256 KiB）")
		}
		if _, err := base64.StdEncoding.DecodeString(m[2]); err != nil {
			return httpx.BadRequest("头像数据无效")
		}
		return nil
	default:
		return httpx.BadRequest("头像格式无效")
	}
}

// setMyAvatar updates the caller's avatar.
func (a *API) setMyAvatar(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Avatar string `json:"avatar"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := validateAvatar(in.Avatar); err != nil {
		return err
	}
	u := userFrom(r.Context())
	u.Avatar = in.Avatar
	if err := a.Store.UpdateUser(r.Context(), u); err != nil {
		return err
	}
	httpx.OK(w, u)
	return nil
}

// userAvatar serves an uploaded avatar image. The URL carries the user's
// updated_at as a version, so it can be cached aggressively.
func (a *API) userAvatar(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathInt64(r, "id")
	if err != nil {
		return err
	}
	u, err := a.Store.GetUser(r.Context(), id)
	if err != nil || !u.AvatarUploaded() {
		return httpx.ErrNotFound
	}
	m := dataURLRe.FindStringSubmatch(u.Avatar)
	if m == nil {
		return httpx.ErrNotFound
	}
	img, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		return httpx.ErrNotFound
	}
	w.Header().Set("Content-Type", m[1])
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
	return nil
}
