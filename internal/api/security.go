package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"ctlvps/internal/httpx"
	"ctlvps/internal/safehttp"
)

type accessPolicy int

const (
	memberAccess accessPolicy = iota
	adminAccess
)

func csrfSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func (a *API) csrfToken(value string) string {
	m := hmac.New(sha256.New, a.csrfKey)
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))
}
func (a *API) sessionName() string {
	if a.Config.SecureCookies {
		return "__Host-ctlvps_session"
	}
	return sessionCookie
}
func (a *API) csrf(w http.ResponseWriter, r *http.Request) error {
	c, err := r.Cookie(a.sessionName())
	if err != nil {
		return httpx.ErrUnauthorized
	}
	httpx.OK(w, map[string]string{"token": a.csrfToken(c.Value)})
	return nil
}
func (a *API) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(a.sessionName())
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(a.csrfToken(c.Value)))
}
func isWrite(r *http.Request) bool {
	return r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS"
}

func (a *API) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Request-ID", hex.EncodeToString(randomID()))
		// Only explicitly trusted adjacent proxies may supply forwarding data.
		trusted := false
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip, _ := netip.ParseAddr(host)
		for _, raw := range a.Config.TrustedProxyCIDRs {
			p, e := netip.ParsePrefix(raw)
			if e == nil && p.Contains(ip) {
				trusted = true
				break
			}
		}
		// A trusted adjacent proxy must overwrite this header, never append
		// caller-supplied values. Only a single authority is supported.
		requestHost := r.Host
		forwardedHosts := r.Header.Values("X-Forwarded-Host")
		if trusted && a.Config.SiteURL != "" && len(forwardedHosts) > 0 {
			requestHost = ""
			if len(forwardedHosts) == 1 {
				requestHost = forwardedHosts[0]
			}
		}
		r.Header.Del("X-Forwarded-Host")
		if !trusted {
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Real-IP")
			r.Header.Del("CF-Connecting-IP")
			r.Header.Del("X-Forwarded-Proto")
		} else {
			// Walk from the adjacent proxy towards the client, stopping at the
			// first untrusted address. Never trust a caller-supplied first item.
			chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
			for i := len(chain) - 1; i >= 0; i-- {
				x, e := netip.ParseAddr(strings.TrimSpace(chain[i]))
				if e != nil {
					break
				}
				host = x.String()
				isProxy := false
				for _, raw := range a.Config.TrustedProxyCIDRs {
					p, e := netip.ParsePrefix(raw)
					if e == nil && p.Contains(x) {
						isProxy = true
						break
					}
				}
				if !isProxy {
					break
				}
			}
			r.RemoteAddr = net.JoinHostPort(host, "0")
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Real-IP")
			r.Header.Del("CF-Connecting-IP")
		}
		expected := a.Config.SiteURL
		if expected == "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			expected = scheme + "://" + r.Host
		}
		target, err := url.Parse(expected)
		if err != nil || target.Host == "" || (!sameSiteHost(target, requestHost) && r.URL.Path != "/healthz") {
			a.securityEvent(r, "host_denied")
			httpx.WriteError(w, httpx.E(421, "invalid_host", "请求域名与站点配置不一致，请检查 CTLVPS_SITE_URL 与反向代理 Host 设置；Nginx 请使用 proxy_set_header Host $http_host;"))
			return
		}
		if r.URL.Path != "/healthz" {
			r.Host = requestHost
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") && isWrite(r) {
			origin, e := url.Parse(r.Header.Get("Origin"))
			if e != nil || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") || safehttp.Origin(origin) != safehttp.Origin(target) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				a.securityEvent(r, "origin_denied")
				httpx.WriteError(w, httpx.E(403, "invalid_origin", "请求来源验证失败，请从面板页面操作"))
				return
			}
			if r.ContentLength != 0 {
				typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if e != nil || typ != "application/json" {
					httpx.WriteError(w, httpx.E(415, "json_required", "需要 JSON 请求"))
					return
				}
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/r/") {
			max := int64(4 << 20)
			if strings.HasPrefix(r.URL.Path, "/api/agent/") {
				max = 2 << 20
				if r.URL.Path == "/api/agent/v1/heartbeat" {
					max = 1 << 20
				}
			}
			rc := http.NewResponseController(w)
			// A read deadline on a bodyless SSE request expires while net/http
			// reads ahead, cancelling the entire keep-alive connection. Apply
			// the body budget only during actual body reads, never while the
			// handler is streaming or doing database work.
			r.Body = http.MaxBytesReader(w, &requestBodyDeadline{ReadCloser: r.Body, controller: rc, deadline: time.Now().Add(20 * time.Second)}, max)
			defer rc.SetReadDeadline(time.Time{})
			defer rc.SetWriteDeadline(time.Time{})
			if r.URL.Path != "/api/v1/events" {
				_ = rc.SetWriteDeadline(time.Now().Add(60 * time.Second))
			}
		}
		if strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/r/") || strings.HasPrefix(r.URL.Path, "/api/v1/auth/") && isWrite(r) {
			if len(r.URL.Path) > 1024 || !a.entryLimiter.Allow(httpx.ClientIP(r, false)) {
				w.Header().Set("Retry-After", "60")
				httpx.WriteError(w, httpx.E(429, "rate_limited", "请求过于频繁"))
				return
			}
		}
		if isWrite(r) && (strings.HasPrefix(r.URL.Path, "/api/v1/auth/") || strings.HasPrefix(r.URL.Path, "/api/v1/users") || strings.Contains(r.URL.Path, "maintenance")) {
			if !a.passwordGate.Acquire() {
				w.Header().Set("Retry-After", "2")
				httpx.WriteError(w, httpx.E(429, "rate_limited", "验证繁忙，请稍后重试"))
				return
			}
			defer a.passwordGate.Release()
		}
		var gate *safehttp.Gate
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/agent/"):
			gate = &a.agentGate
			if !a.agentLimiter.Allow(httpx.ClientIP(r, false)) {
				httpx.WriteError(w, httpx.E(429, "rate_limited", "设备请求过于频繁"))
				return
			}
		case strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/r/"):
			gate = &a.subscriptionGate
		case strings.HasPrefix(r.URL.Path, "/api/v1/") && r.URL.Path != "/api/v1/events":
			gate = &a.apiGate
		}
		if gate != nil {
			if !gate.Acquire() {
				w.Header().Set("Retry-After", "2")
				httpx.WriteError(w, httpx.E(429, "busy", "请求繁忙"))
				return
			}
			defer gate.Release()
		}
		if a.Config.SecureCookies {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
func randomID() []byte { b := make([]byte, 12); _, _ = rand.Read(b); return b }

// sameSiteHost compares authorities using the configured public scheme, not
// the proxy's HTTP transport or an untrusted forwarded-proto header.
func sameSiteHost(target *url.URL, authority string) bool {
	if authority == "" || strings.ContainsAny(authority, "/\\@?#%, \t\r\n") || strings.HasSuffix(authority, ":") {
		return false
	}
	u, err := url.Parse(target.Scheme + "://" + authority)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if strings.Contains(u.Hostname(), ":") && !strings.HasPrefix(authority, "[") {
		return false
	}
	if strings.HasPrefix(authority, "[") {
		ip, err := netip.ParseAddr(u.Hostname())
		if err != nil || !ip.Is6() {
			return false
		}
	}
	return safehttp.Origin(u) == safehttp.Origin(target)
}

// Fixed reason keys and a small budget keep attack logging bounded. Never log
// raw URLs, origin values, cookies, request bodies or credentials.
func (a *API) securityEvent(r *http.Request, reason string) {
	if a.securityLogLimiter.Allow(reason) {
		a.Logger.Warn("security request rejected", "reason", reason, "route", r.Pattern)
	}
}

// requestBodyDeadline bounds the total body-read window without leaving a
// socket deadline armed after decoding. Keep-alive and SSE share that socket.
type requestBodyDeadline struct {
	io.ReadCloser
	controller *http.ResponseController
	deadline   time.Time
}

func (b *requestBodyDeadline) Read(p []byte) (int, error) {
	_ = b.controller.SetReadDeadline(b.deadline)
	defer b.controller.SetReadDeadline(time.Time{})
	return b.ReadCloser.Read(p)
}
