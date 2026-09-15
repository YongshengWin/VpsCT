// Package api exposes the REST/SSE API of ctlvpsd, the public subscription
// endpoints and the agent protocol.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/assets"
	"ctlvps/internal/auth"
	"ctlvps/internal/connlog"
	"ctlvps/internal/corecatalog"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/geoip"
	"ctlvps/internal/httpx"
	"ctlvps/internal/notify"
	"ctlvps/internal/scheduler"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"ctlvps/internal/traffic"
)

// Config holds runtime options for the API.
type Config struct {
	SiteURL       string // external base URL for subscription links; falls back to request host
	TrustProxy    bool
	SecureCookies bool
	SessionTTL    time.Duration
	Version       string
	StartedAt     time.Time
	DataDir       string
	AgentBinDir   string // where ctlvps-agent-linux-{arch} binaries live
	SetupToken    string // local first-run capability; never exposed by the API
}

// Deps wires the API to the services.
type Deps struct {
	Maintenance MaintenanceClient
	Store       *store.Store
	Connlog     *connlog.Store
	Geo         *geoip.Lookup
	Subs        *subscription.Service
	Desired     *desired.Builder
	Shares      *share.Manager
	Traffic     *traffic.Ingestor
	Notify      *notify.Telegram
	Scheduler   *scheduler.Scheduler
	Logger      *slog.Logger
	Static      http.Handler
	Config      Config
}

// API is the HTTP surface.
type API struct {
	Deps
	mux           *http.ServeMux
	Events        *EventBus
	challenges    *challengeStore // logins waiting for their second factor
	loginLimiter  *rateLimiter    // password attempts per IP
	factorLimiter *rateLimiter    // second-factor attempts per IP
	cores         *corecatalog.Fetcher
}

const (
	sessionCookie = "ctlvps_session"
	// defaultSiteName is the product name shown until an admin sets site.name.
	defaultSiteName = "VpsCT"
)

// New builds the API and registers routes.
func New(d Deps) *API {
	if d.Config.SessionTTL == 0 {
		d.Config.SessionTTL = 30 * 24 * time.Hour
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	a := &API{
		Deps:          d,
		mux:           http.NewServeMux(),
		Events:        NewEventBus(),
		challenges:    newChallengeStore(),
		loginLimiter:  newRateLimiter(10, time.Minute),
		factorLimiter: newRateLimiter(30, time.Minute),
		cores:         &corecatalog.Fetcher{},
	}
	a.routes()
	return a
}

// Handler returns the root handler.
func (a *API) Handler() http.Handler {
	return a.withCommon(a.mux)
}

func (a *API) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("X-Frame-Options", "DENY")
		}
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/agent/") && r.URL.Path != "/api/v1/events" {
			a.Logger.Debug("http", "method", r.Method, "path", r.URL.Path, "took", time.Since(start))
		}
	})
}

// ---- context / auth ----

type ctxKey int

const userKey ctxKey = 1

func userFrom(ctx context.Context) *domain.User {
	u, _ := ctx.Value(userKey).(*domain.User)
	return u
}

func (a *API) currentUser(r *http.Request) (*domain.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, httpx.ErrUnauthorized
	}
	sess, err := a.Store.GetSession(r.Context(), auth.HashToken(c.Value))
	if err != nil {
		return nil, httpx.ErrUnauthorized
	}
	u, err := a.Store.GetUser(r.Context(), sess.UserID)
	if err != nil || !u.Enabled {
		return nil, httpx.ErrUnauthorized
	}
	return &u, nil
}

// handle registers an authenticated handler; admin restricts to admins.
func (a *API) handle(pattern string, admin bool, h httpx.Handler) {
	a.mux.Handle(pattern, httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		u, err := a.currentUser(r)
		if err != nil {
			return err
		}
		if admin && u.Role != domain.RoleAdmin {
			return httpx.ErrForbidden
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		return h(w, r.WithContext(ctx))
	}))
}

// public registers an unauthenticated handler.
func (a *API) public(pattern string, h httpx.Handler) {
	a.mux.Handle(pattern, h)
}

func (a *API) audit(r *http.Request, action, target string, detail any) {
	u := userFrom(r.Context())
	e := domain.AuditEvent{Action: action, Target: target, IP: httpx.ClientIP(r, a.Config.TrustProxy)}
	if u != nil {
		id := u.ID
		e.UserID = &id
		e.Username = u.Username
	}
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			e.Detail = b
		}
	}
	if err := a.Store.AddAudit(r.Context(), e); err != nil {
		a.Logger.Warn("audit write failed", "err", err)
	}
}

func isAdmin(u *domain.User) bool { return u != nil && u.Role == domain.RoleAdmin }

func (a *API) ctx() context.Context { return context.Background() }

// baseURL returns the public base URL for links.
func (a *API) baseURL(r *http.Request) string {
	if a.Config.SiteURL != "" {
		return strings.TrimRight(a.Config.SiteURL, "/")
	}
	if v := a.Store.GetSetting(r.Context(), domain.SettingSiteURL, ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- SSE event bus ----

// Event is a server-sent event.
type Event struct {
	Type string    `json:"type"`
	Data any       `json:"data"`
	TS   time.Time `json:"ts"`
}

// EventBus fans events out to SSE subscribers.
type EventBus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewEventBus builds a bus.
func NewEventBus() *EventBus { return &EventBus{subs: map[chan Event]struct{}{}} }

// Publish sends to all subscribers (non-blocking).
func (b *EventBus) Publish(typ string, data any) {
	ev := Event{Type: typ, Data: data, TS: time.Now().UTC()}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe registers a channel; call the returned func to leave.
func (b *EventBus) Subscribe() (chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// ---- routes ----

func (a *API) routes() {
	m := a.mux
	// auth
	a.public("GET /api/v1/auth/setup", a.setupStatus)
	a.public("POST /api/v1/auth/setup", a.setup)
	a.public("POST /api/v1/auth/login", a.login)
	a.public("POST /api/v1/auth/login/2fa", a.login2FA)
	a.public("POST /api/v1/auth/logout", a.logout)
	a.handle("GET /api/v1/auth/me", false, a.me)
	a.handle("POST /api/v1/auth/password", false, a.changePassword)
	a.handle("PUT /api/v1/auth/avatar", false, a.setMyAvatar)
	a.handle("PUT /api/v1/auth/profile", false, a.setMyProfile)
	a.handle("POST /api/v1/auth/2fa/setup", false, a.twoFASetup)
	a.handle("POST /api/v1/auth/2fa/enable", false, a.twoFAEnable)
	a.handle("POST /api/v1/auth/2fa/disable", false, a.twoFADisable)
	a.handle("POST /api/v1/auth/2fa/recovery", false, a.twoFARecovery)

	// users
	a.handle("GET /api/v1/users", true, a.listUsers)
	a.handle("POST /api/v1/users", true, a.createUser)
	a.handle("PUT /api/v1/users/{id}", true, a.updateUser)
	a.handle("DELETE /api/v1/users/{id}", true, a.deleteUser)
	a.handle("GET /api/v1/users/{id}/avatar", false, a.userAvatar)
	a.handle("POST /api/v1/users/{id}/2fa/reset", true, a.twoFAReset)

	// servers
	a.handle("GET /api/v1/servers", true, a.listServers)
	a.handle("POST /api/v1/servers", true, a.createServer)
	a.handle("GET /api/v1/servers/{id}", true, a.getServer)
	a.handle("PUT /api/v1/servers/{id}", true, a.updateServer)
	a.handle("DELETE /api/v1/servers/{id}", true, a.deleteServer)
	a.handle("POST /api/v1/servers/{id}/enroll-token", true, a.enrollToken)
	a.handle("POST /api/v1/servers/{id}/reset-token", true, a.resetAgentToken)
	a.handle("GET /api/v1/servers/{id}/traffic", true, a.serverTraffic)
	a.handle("GET /api/v1/servers/{id}/samples", true, a.serverSamples)
	a.handle("GET /api/v1/servers/{id}/desired", true, a.serverDesired)
	a.handle("POST /api/v1/servers/{id}/republish", true, a.serverRepublish)
	a.handle("POST /api/v1/servers/{id}/update-agent", true, a.updateAgent)
	a.handle("GET /api/v1/servers/{id}/maintenance", true, a.serverMaintenance)
	a.handle("POST /api/v1/servers/{id}/maintenance", true, a.startAgentMaintenance)
	a.handle("GET /api/v1/system/maintenance", true, a.controllerMaintenance)
	a.handle("GET /api/v1/system/maintenance/latest", true, a.latestController)
	a.handle("POST /api/v1/system/maintenance", true, a.startControllerMaintenance)
	a.public("POST /api/maintenance/v1/jobs/{job}/claim", a.claimMaintenance)
	a.public("POST /api/maintenance/v1/jobs/{job}/report", a.reportMaintenance)
	a.handle("POST /api/v1/agents/update", true, a.updateAllAgents)
	a.handle("POST /api/v1/servers/{id}/nodes", true, a.deployNode)

	// nodes
	a.handle("GET /api/v1/nodes", true, a.listNodes)
	a.handle("POST /api/v1/nodes", true, a.createNode)
	a.handle("POST /api/v1/nodes/parse", true, a.parseNodes)
	a.handle("POST /api/v1/nodes/import", true, a.importNodes)
	a.handle("POST /api/v1/nodes/reorder", true, a.reorderNodes)
	a.handle("POST /api/v1/nodes/bulk-delete", true, a.bulkDeleteNodes)
	a.handle("POST /api/v1/nodes/chain", true, a.setNodeChain)
	a.handle("GET /api/v1/nodes/{id}", true, a.getNode)
	a.handle("PUT /api/v1/nodes/{id}", true, a.updateNode)
	a.handle("DELETE /api/v1/nodes/{id}", true, a.deleteNode)
	a.handle("GET /api/v1/nodes/{id}/uri", true, a.nodeURI)
	a.handle("GET /api/v1/nodes/{id}/traffic", true, a.nodeTraffic)
	a.handle("POST /api/v1/nodes/{id}/regenerate", true, a.regenerateNode)

	// external subscriptions
	a.handle("GET /api/v1/externals", true, a.listExternals)
	a.handle("POST /api/v1/externals", true, a.createExternal)
	a.handle("PUT /api/v1/externals/{id}", true, a.updateExternal)
	a.handle("DELETE /api/v1/externals/{id}", true, a.deleteExternal)
	a.handle("POST /api/v1/externals/{id}/sync", true, a.syncExternal)
	a.handle("POST /api/v1/externals/{id}/import-body", true, a.importExternalBody)
	a.handle("GET /api/v1/externals/{id}/traffic", true, a.externalTraffic)

	// subscriptions
	a.handle("GET /api/v1/subscriptions", false, a.listSubscriptions)
	a.handle("POST /api/v1/subscriptions", true, a.createSubscription)
	a.handle("POST /api/v1/subscriptions/preview", true, a.previewSubscription)
	a.handle("POST /api/v1/subscriptions/validate-groups", true, a.validateGroups)
	a.handle("GET /api/v1/subscriptions/{id}", false, a.getSubscription)
	a.handle("PUT /api/v1/subscriptions/{id}", true, a.updateSubscription)
	a.handle("DELETE /api/v1/subscriptions/{id}", true, a.deleteSubscription)
	a.handle("POST /api/v1/subscriptions/{id}/rotate-token", true, a.rotateSubscriptionToken)
	a.handle("GET /api/v1/subscriptions/{id}/render", false, a.renderSubscription)
	a.handle("GET /api/v1/subscriptions/{id}/access-log", true, a.subscriptionAccessLog)

	// templates & presets
	a.handle("GET /api/v1/templates", false, a.listTemplates)
	a.handle("POST /api/v1/templates", true, a.createTemplate)
	a.handle("PUT /api/v1/templates/{id}", true, a.updateTemplate)
	a.handle("DELETE /api/v1/templates/{id}", true, a.deleteTemplate)
	a.handle("GET /api/v1/presets", false, a.listPresets)
	a.handle("POST /api/v1/presets", true, a.createPreset)
	a.handle("PUT /api/v1/presets/{id}", true, a.updatePreset)
	a.handle("DELETE /api/v1/presets/{id}", true, a.deletePreset)

	// shares
	a.handle("GET /api/v1/shares", false, a.listShares)
	a.handle("POST /api/v1/shares", true, a.createShare)
	a.handle("GET /api/v1/shares/{id}", false, a.getShare)
	a.handle("PUT /api/v1/shares/{id}", true, a.updateShare)
	a.handle("PUT /api/v1/shares/{id}/connlog", true, a.setShareConnlog)
	a.handle("DELETE /api/v1/shares/{id}", true, a.deleteShare)
	a.handle("POST /api/v1/shares/{id}/pause", true, a.shareAction("pause"))
	a.handle("POST /api/v1/shares/{id}/resume", true, a.shareAction("resume"))
	a.handle("POST /api/v1/shares/{id}/reset", true, a.shareAction("reset"))
	a.handle("POST /api/v1/shares/{id}/revoke", true, a.shareAction("revoke"))
	a.handle("POST /api/v1/shares/{id}/reissue", true, a.shareAction("reissue"))
	a.handle("GET /api/v1/shares/{id}/events", false, a.shareEvents)
	a.handle("GET /api/v1/shares/{id}/traffic", false, a.shareTraffic)

	// connection logs
	a.handle("GET /api/v1/connlog", true, a.queryConnlog)
	a.handle("GET /api/v1/connlog/top", true, a.topDomains)
	a.handle("GET /api/v1/connlog/summary", true, a.connlogSummary)
	a.handle("GET /api/v1/connlog/export", true, a.exportConnlog)
	a.handle("GET /api/v1/connlog/stats", true, a.connlogStats)
	a.handle("DELETE /api/v1/connlog/shares/{id}", true, a.deleteShareConnlog)

	// dashboard / settings / system
	a.handle("GET /api/v1/dashboard", false, a.dashboard)
	a.handle("GET /api/v1/traffic/overview", true, a.trafficOverview)
	a.handle("GET /api/v1/settings", true, a.getSettings)
	a.handle("GET /api/v1/settings/core-versions", true, a.coreVersions)
	a.handle("PUT /api/v1/settings", true, a.putSettings)
	a.handle("POST /api/v1/settings/telegram/test", true, a.testTelegram)
	a.handle("GET /api/v1/audit", true, a.listAudit)
	a.handle("GET /api/v1/bans", true, a.listBans)
	a.handle("POST /api/v1/bans", true, a.createBan)
	a.handle("PUT /api/v1/bans/{id}", true, a.updateBan)
	a.handle("DELETE /api/v1/bans/{id}", true, a.deleteBan)
	a.handle("GET /api/v1/access-log", true, a.accessLog)
	a.handle("GET /api/v1/system/status", true, a.systemStatus)
	a.handle("GET /api/v1/events", false, a.events)
	a.handle("GET /api/v1/meta", false, a.meta)

	// agent protocol
	a.public("POST /api/agent/v1/enroll", a.agentEnroll)
	a.agentRoute("POST /api/agent/v1/heartbeat", a.agentHeartbeat)
	a.agentRoute("GET /api/agent/v1/desired", a.agentDesired)
	a.agentRoute("POST /api/agent/v1/apply-report", a.agentApplyReport)
	a.agentRoute("POST /api/agent/v1/connlog", a.agentConnlog)

	// public subscription endpoints
	a.public("GET /s/{token}", a.publicSubscription)
	a.public("GET /s/{token}/{format}", a.publicSubscription)
	a.public("GET /r/{code}", a.publicShort)
	a.public("GET /healthz", func(w http.ResponseWriter, r *http.Request) error {
		httpx.OK(w, map[string]any{"ok": true, "version": a.Config.Version})
		return nil
	})
	a.public("GET /install-agent.sh", func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		_, _ = w.Write(assets.InstallAgent)
		return nil
	})
	a.public("GET /dl/agent/{platform}", a.downloadAgent)

	// 404 for unknown API paths, SPA for everything else
	m.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, httpx.ErrNotFound)
	}))
	if a.Static != nil {
		m.Handle("/", a.Static)
	}
}
