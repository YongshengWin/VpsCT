package api

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ctlvps/internal/auth"
	"ctlvps/internal/domain"
	"ctlvps/internal/httpx"
	"ctlvps/internal/subscription"
)

var subLimiter = newRateLimiter(60, time.Minute)

// banned checks ban rules for the request.
func (a *API) banned(r *http.Request, ip string) bool {
	rules, err := a.Store.ListBans(r.Context())
	if err != nil {
		return false
	}
	now := a.Store.Now()
	parsed := net.ParseIP(ip)
	ua := strings.ToLower(r.UserAgent())
	for _, b := range rules {
		if !b.Enabled || (b.ExpiresAt != nil && now.After(*b.ExpiresAt)) {
			continue
		}
		switch b.Kind {
		case "ip":
			if b.Value == ip {
				return true
			}
		case "cidr":
			if _, n, err := net.ParseCIDR(b.Value); err == nil && parsed != nil && n.Contains(parsed) {
				return true
			}
		case "ua":
			if strings.Contains(ua, strings.ToLower(b.Value)) {
				return true
			}
		}
	}
	return false
}

// publicSubscription serves /s/{token}[/{format}].
func (a *API) publicSubscription(w http.ResponseWriter, r *http.Request) error {
	token := r.PathValue("token")
	format := r.PathValue("format")
	return a.serve(w, r, token, format, false)
}

// publicShort serves /r/{code}.
func (a *API) publicShort(w http.ResponseWriter, r *http.Request) error {
	code := r.PathValue("code")
	sub, err := a.Store.GetSubscriptionByShortCode(r.Context(), code)
	if err != nil {
		return httpx.ErrNotFound
	}
	return a.serveSub(w, r, sub, r.URL.Query().Get("format"), true)
}

func (a *API) serve(w http.ResponseWriter, r *http.Request, token, format string, short bool) error {
	sub, err := a.Store.GetSubscriptionByTokenHash(r.Context(), auth.HashToken(token))
	if err != nil {
		// constant-ish response for unknown tokens
		time.Sleep(150 * time.Millisecond)
		return httpx.ErrNotFound
	}
	if format == "" {
		format = r.URL.Query().Get("format")
	}
	return a.serveSub(w, r, sub, format, short)
}

func (a *API) serveSub(w http.ResponseWriter, r *http.Request, sub domain.Subscription, format string, short bool) error {
	ctx := r.Context()
	ip := httpx.ClientIP(r, a.Config.TrustProxy)
	logEntry := domain.AccessLog{SubscriptionID: sub.ID, IP: ip, UserAgent: r.UserAgent()}
	fail := func(status int, code, msg string) error {
		logEntry.Status = status
		_ = a.Store.AddAccessLog(ctx, logEntry)
		return httpx.E(status, code, msg)
	}
	if a.banned(r, ip) {
		return fail(http.StatusForbidden, "banned", "forbidden")
	}
	limit := a.Store.GetSettingInt(ctx, domain.SettingRateLimitPerMin, 60)
	if !subLimiter.AllowN(ip, limit) {
		return fail(http.StatusTooManyRequests, "rate_limited", "too many requests")
	}
	if !sub.Kind.Supported() {
		return fail(http.StatusGone, "unsupported_kind", "subscription type retired")
	}
	if !sub.Enabled {
		return fail(http.StatusGone, "disabled", "subscription disabled")
	}
	if sub.ExpireAt != nil && a.Store.Now().After(*sub.ExpireAt) {
		return fail(http.StatusGone, "expired", "subscription expired")
	}
	if sub.ShareID != nil {
		sh, err := a.Store.GetShare(ctx, *sub.ShareID)
		if err != nil || sh.Status == domain.ShareRevoked {
			return fail(http.StatusGone, "revoked", "subscription revoked")
		}
	}
	if subscriptionBrowserRequest(r) {
		logEntry.Status = http.StatusForbidden
		_ = a.Store.AddAccessLog(ctx, logEntry)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(subscriptionBrowserPage))
		return nil
	}
	if format == "" {
		format = subscription.DetectFormat(r.UserAgent())
	}
	if format == "" {
		format = sub.DefaultFormat
	}
	rendered, bundle, err := a.Subs.Render(ctx, sub, format)
	if err != nil {
		a.Logger.Warn("render subscription", "sub", sub.ID, "err", err)
		return fail(http.StatusInternalServerError, "render_failed", "render failed")
	}
	logEntry.Format = rendered.Format
	logEntry.Status = http.StatusOK
	_ = a.Store.AddAccessLog(ctx, logEntry)

	w.Header().Set("Content-Type", rendered.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Profile-Update-Interval", "6")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="clash%s"; filename*=UTF-8''%s`, extFor(rendered.Format), encodeFilename(sub.Name, rendered.Format)))
	if sub.UserinfoHeader && bundle.Userinfo != nil {
		w.Header().Set("Subscription-Userinfo", bundle.Userinfo.Header())
	}
	if title := sub.Name; title != "" {
		w.Header().Set("Profile-Title", "base64:"+base64Std(title))
	}
	if short {
		w.Header().Set("X-Short-Link", "1")
	}
	body := rendered.Body
	if rendered.Format == subscription.FormatSurge {
		body = surgeManagedBody(body, a.baseURL(r), r.URL)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}

// Browser navigation must not download a profile, including explicit-format
// and short URLs. Non-browser fetchers keep their existing format behavior.
func subscriptionBrowserRequest(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Mode"), "navigate") || strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document") {
		return true
	}
	// Some subscription clients include browser tokens in their UA.
	if subscription.DetectFormat(r.UserAgent()) != "" {
		return false
	}
	return strings.Contains(strings.ToLower(r.UserAgent()), "mozilla/")
}

//go:embed subscription_browser.html
var subscriptionBrowserPage string

// Use this request's subscription capability, never a URL embedded in a
// shared template. Pin the format so background updates need no Surge UA.
func surgeManagedBody(body []byte, base string, requestURL *url.URL) []byte {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || strings.ContainsAny(base, "\r\n\t ") {
		return body
	}
	u.Path = strings.TrimRight(u.Path, "/") + requestURL.Path
	u.RawPath = ""
	u.RawQuery, u.Fragment = "", ""
	if !strings.HasSuffix(requestURL.Path, "/surge") {
		u.RawQuery = "format=surge"
	}
	// A custom template may already declare a managed URL. Replace it rather
	// than emitting competing declarations or retaining another subscription.
	lines := strings.Split(strings.TrimPrefix(string(body), "\ufeff"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#!MANAGED-CONFIG") {
			kept = append(kept, line)
		}
	}
	return []byte("#!MANAGED-CONFIG " + u.String() + " interval=3600 strict=false\n" + strings.Join(kept, "\n"))
}

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func extFor(format string) string {
	return map[string]string{"mihomo": ".yaml", "surge": ".conf", "shadowrocket": ".conf", "raw": ".txt", "uri": ".txt", "singbox": ".json"}[format]
}

func encodeFilename(name, format string) string {
	ext := extFor(format)
	var b strings.Builder
	for _, r := range name {
		if r < 0x80 && (r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			b.WriteRune(r)
		} else {
			for _, c := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
	}
	return b.String() + ext
}
