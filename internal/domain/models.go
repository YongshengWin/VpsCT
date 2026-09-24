// Package domain defines the persistent entities shared by the control
// server, the API layer and (through agentproto) the agent.
package domain

import (
	"ctlvps/internal/networkconfig"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Role is an account role.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// User is a login account. Normal users only see subscriptions and shares
// that are bound to them.
type User struct {
	SecurityVersion int64  `json:"-"`
	ID              int64  `json:"id"`
	Username        string `json:"username"`
	Nickname        string `json:"nickname"`
	PasswordHash    string `json:"-"`
	Role            Role   `json:"role"`
	Enabled         bool   `json:"enabled"`
	// Avatar is "" (initials), "preset:<id>" (built-in SVG drawn by the web
	// client) or an uploaded "data:image/...;base64,..." URL. Uploaded images
	// are never inlined in JSON; MarshalJSON swaps them for the avatar endpoint.
	Avatar        string     `json:"avatar"`
	TOTPEnabled   bool       `json:"totp_enabled"`
	TOTPSecret    string     `json:"-"` // base32; set but not enabled = pending setup
	TOTPLastStep  int64      `json:"-"` // last accepted time step, rejects code replay
	RecoveryCodes []string   `json:"-"` // sha256 hex of unused one-time codes
	LastLoginAt   *time.Time `json:"last_login_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// DisplayName is the nickname when set, otherwise the login username.
func (u User) DisplayName() string {
	if n := strings.TrimSpace(u.Nickname); n != "" {
		return n
	}
	return u.Username
}

// AvatarUploaded reports whether the avatar is an uploaded image (data URL).
func (u User) AvatarUploaded() bool { return strings.HasPrefix(u.Avatar, "data:") }

// AvatarURL is the client-facing avatar value: presets pass through, uploaded
// images become a versioned URL served by the API.
func (u User) AvatarURL() string {
	if u.AvatarUploaded() {
		return fmt.Sprintf("/api/v1/users/%d/avatar?v=%d", u.ID, u.UpdatedAt.Unix())
	}
	return u.Avatar
}

// MarshalJSON hides secrets and replaces inline image data with a URL.
func (u User) MarshalJSON() ([]byte, error) {
	type alias User
	return json.Marshal(struct {
		alias
		Avatar      string `json:"avatar"`
		DisplayName string `json:"display_name"`
	}{alias(u), u.AvatarURL(), u.DisplayName()})
}

// CoreMode selects how proxy cores are laid out on one VPS.
//   - stable: Snell runs on the official snell-server, everything else on sing-box.
//   - lean:   everything (including Snell) runs inside one sing-box process.
type CoreMode string

const (
	CoreModeStable CoreMode = "stable"
	CoreModeLean   CoreMode = "lean"
)

// Server is a VPS managed (or at least observed) by ctlvps.
type Server struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Region        string    `json:"region"` // two-letter country/region code
	PublicHost    string    `json:"public_host"`
	Tags          []string  `json:"tags"`
	Notes         string    `json:"notes"`
	QuotaBytes    int64     `json:"quota_bytes"`     // 0 = unlimited
	QuotaResetDay int       `json:"quota_reset_day"` // 1..28, or 31 = last day (29/30/31 normalize to 31)
	QuotaBilling  string    `json:"quota_billing"`   // kept for compat; quota always uses inbound+outbound
	CoreMode      CoreMode  `json:"core_mode"`
	IPv4Only      bool      `json:"ipv4_only"`
	CertMode      string    `json:"cert_mode"` // self_signed|acme|external
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// AgentStatus is derived from the last heartbeat.
type AgentStatus string

const (
	AgentPending AgentStatus = "pending"
	AgentOnline  AgentStatus = "online"
	AgentOffline AgentStatus = "offline"
)

// Agent is the enrolment and liveness record of the ctlvps-agent on a server.
type Agent struct {
	ID              int64           `json:"id"`
	ServerID        int64           `json:"server_id"`
	TokenHash       string          `json:"-"`
	EnrollTokenHash string          `json:"-"`
	EnrollExpiresAt *time.Time      `json:"enroll_expires_at,omitempty"`
	Version         string          `json:"version"`
	LastSeenAt      *time.Time      `json:"last_seen_at,omitempty"`
	AppliedRevision int64           `json:"applied_revision"`
	AppliedHash     string          `json:"applied_hash"`
	ApplyError      string          `json:"apply_error"`
	PublicIPv4      string          `json:"public_ipv4"`
	PublicIPv6      string          `json:"public_ipv6"`
	Metrics         json.RawMessage `json:"metrics,omitempty"`
	Diagnostics     json.RawMessage `json:"diagnostics,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// NodeSource says where a node came from.
type NodeSource string

const NodeTransit NodeSource = "transit"

const (
	NodeManual   NodeSource = "manual"   // typed or pasted by the operator
	NodeImported NodeSource = "imported" // pulled from an external subscription
	NodeDeployed NodeSource = "deployed" // created by ctlvps on a managed VPS
	NodeChain    NodeSource = "chain"    // virtual: landing dialed through a front
)

// Core is the proxy core that serves a deployed node.
type Core string

const (
	CoreNone    Core = ""
	CoreSingBox Core = "singbox"
	CoreSnell   Core = "snell"
	CoreMita    Core = "mieru"
)

// Node is a proxy endpoint. Params holds the client-facing protocol
// parameters using Clash/mihomo field names (uuid, password, tls, sni,
// network, ws-opts, reality-opts, ...). ServerParams holds server-only
// secrets (reality private key, certificate mode) and is never rendered to
// subscriptions.
type Node struct {
	Network          *networkconfig.Node `json:"network,omitempty"`
	NetworkRevision  int64               `json:"network_revision,omitempty"`
	ID               int64               `json:"id"`
	Name             string              `json:"name"`
	Protocol         string              `json:"protocol"`
	Server           string              `json:"server"`
	Port             int                 `json:"port"`
	Params           json.RawMessage     `json:"params"`
	ServerParams     json.RawMessage     `json:"-"`
	Source           NodeSource          `json:"source"`
	ServerID         *int64              `json:"server_id,omitempty"`
	ListenPort       int                 `json:"listen_port,omitempty"`
	Core             Core                `json:"core"`
	ShareID          *int64              `json:"share_id,omitempty"`
	ExternalSubID    *int64              `json:"external_sub_id,omitempty"`
	ChainFrontNodeID *int64              `json:"chain_front_node_id,omitempty"`
	Enabled          bool                `json:"enabled"`
	OwnerUserID      int64               `json:"owner_user_id"`
	Tags             []string            `json:"tags"`
	SortOrder        int                 `json:"sort_order"`
	Revoked          bool                `json:"revoked"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

// ExternalSubscription is an "airport" subscription URL that we pull nodes
// and subscription-userinfo from.
type ExternalSubscription struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	UserAgent       string     `json:"user_agent"`
	SyncIntervalMin int        `json:"sync_interval_min"`
	LastSyncAt      *time.Time `json:"last_sync_at,omitempty"`
	LastError       string     `json:"last_error"`
	Upload          int64      `json:"upload"`
	Download        int64      `json:"download"`
	Total           int64      `json:"total"`
	ExpireAt        *time.Time `json:"expire_at,omitempty"`
	NodeCount       int        `json:"node_count"`
	Enabled         bool       `json:"enabled"`
	OwnerUserID     int64      `json:"owner_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// SubscriptionKind distinguishes how a subscription's content is produced.
type SubscriptionKind string

const (
	SubImported  SubscriptionKind = "imported"  // mirrors an external subscription (live aggregate)
	SubGenerated SubscriptionKind = "generated" // built from nodes + groups + rules + template
	SubShare     SubscriptionKind = "share"     // owned by a share; nodes come from the share
)

// Supported reports whether this subscription kind can still produce content.
func (k SubscriptionKind) Supported() bool {
	return k == SubGenerated || k == SubImported || k == SubShare
}

// ProxyGroup is one policy group of a generated subscription.
type ProxyGroup struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"` // select|url-test|fallback|load-balance|relay
	Proxies        []string `json:"proxies"`
	NodeIDs        []int64  `json:"node_ids,omitempty"`
	IncludeAll     bool     `json:"include_all,omitempty"`
	Filter         string   `json:"filter,omitempty"` // regex on node name
	URL            string   `json:"url,omitempty"`
	Interval       int      `json:"interval,omitempty"`
	Tolerance      int      `json:"tolerance,omitempty"`
	Lazy           bool     `json:"lazy,omitempty"`
	Icon           string   `json:"icon,omitempty"`
	Hidden         bool     `json:"hidden,omitempty"`
	DialerProxy    string   `json:"dialer_proxy,omitempty"` // chain: front group/node
	ExcludeFilter  string   `json:"exclude_filter,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
	DisableUDP     bool     `json:"disable_udp,omitempty"`
	InterfaceName  string   `json:"interface_name,omitempty"`
	RoutingMark    int      `json:"routing_mark,omitempty"`
	ExpectedStatus string   `json:"expected_status,omitempty"`
}

// MarshalJSON guarantees "proxies" is always a JSON array (never null) so
// API clients can rely on it without nil checks.
func (g ProxyGroup) MarshalJSON() ([]byte, error) {
	type plain ProxyGroup
	p := plain(g)
	if p.Proxies == nil {
		p.Proxies = []string{}
	}
	return json.Marshal(p)
}

// ChainSpec declares a chained proxy: Landing dialed through Front.
type ChainSpec struct {
	Name          string `json:"name"`
	FrontNodeID   int64  `json:"front_node_id"`
	LandingNodeID int64  `json:"landing_node_id"`
}

// NodeSelection is the set of sources a generated subscription draws from.
type NodeSelection struct {
	IncludeAll     bool     `json:"include_all,omitempty"` // every enabled node except share-dedicated ones
	NodeIDs        []int64  `json:"node_ids"`
	ExternalSubIDs []int64  `json:"external_sub_ids"`
	Tags           []string `json:"tags,omitempty"`
	Filter         string   `json:"filter,omitempty"`
	ExcludeFilter  string   `json:"exclude_filter,omitempty"`
}

// Subscription is a shareable link (/s/<token>/<format>).
type Subscription struct {
	ID                int64            `json:"id"`
	Name              string           `json:"name"`
	Kind              SubscriptionKind `json:"kind"`
	Token             string           `json:"token"` // capability secret; only returned to admins/owners
	TokenHash         string           `json:"-"`
	TokenHint         string           `json:"token_hint"` // first 6 chars, for display
	ShortCode         string           `json:"short_code"`
	TemplateID        *int64           `json:"template_id,omitempty"`
	DefaultFormat     string           `json:"default_format"`
	ProxyGroups       []ProxyGroup     `json:"proxy_groups"`
	Chains            []ChainSpec      `json:"chains"`
	Rules             []string         `json:"rules"`
	RuleProviders     json.RawMessage  `json:"rule_providers,omitempty"`
	NodeSelection     NodeSelection    `json:"node_selection"`
	SourceExternalID  *int64           `json:"source_external_id,omitempty"`
	ExpireAt          *time.Time       `json:"expire_at,omitempty"`
	TrafficLimitBytes int64            `json:"traffic_limit_bytes"`
	ResetDay          int              `json:"reset_day"` // 1..28, or 31 = last day; 0 = never
	UserinfoHeader    bool             `json:"userinfo_header"`
	ShowInfoNodes     bool             `json:"show_info_nodes"`
	OwnerUserID       int64            `json:"owner_user_id"`
	AllowedUserIDs    []int64          `json:"allowed_user_ids"`
	ShareID           *int64           `json:"share_id,omitempty"`
	Enabled           bool             `json:"enabled"`
	AccessCount       int64            `json:"access_count"`
	LastAccessAt      *time.Time       `json:"last_access_at,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// RuleTemplate is a client configuration template with ctlvps markers.
type RuleTemplate struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"` // mihomo|surge|singbox|shadowrocket
	Description string          `json:"description"`
	Content     string          `json:"content"`
	Variables   json.RawMessage `json:"variables,omitempty"`
	IsBuiltin   bool            `json:"is_builtin"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ProxyGroupPreset is a reusable group layout for the generator.
type ProxyGroupPreset struct {
	ID        int64        `json:"id"`
	Name      string       `json:"name"`
	Groups    []ProxyGroup `json:"groups"`
	Rules     []string     `json:"rules"`
	IsBuiltin bool         `json:"is_builtin"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// ShareStatus is the lifecycle of a share.
type ShareStatus string

const (
	ShareActive    ShareStatus = "active"
	SharePaused    ShareStatus = "paused"
	ShareExhausted ShareStatus = "exhausted"
	ShareExpired   ShareStatus = "expired"
	ShareRevoked   ShareStatus = "revoked"
)

// ShareTarget selects which protocols a share gets on one server.
type ShareTarget struct {
	Network   *networkconfig.Node `json:"network,omitempty"`
	ServerID  int64               `json:"server_id"`
	Protocols []string            `json:"protocols"`
}

// Share is a customer allotment: dedicated inbounds on chosen servers plus
// a metered quota and a subscription.
type Share struct {
	ID             int64         `json:"id"`
	Name           string        `json:"name"`
	UserID         *int64        `json:"user_id,omitempty"`
	Targets        []ShareTarget `json:"targets"`
	ExtraNodeIDs   []int64       `json:"extra_node_ids"` // imported nodes mixed in (soft limit only)
	QuotaBytes     int64         `json:"quota_bytes"`    // 0 = unlimited
	BillingMode    string        `json:"billing_mode"`   // kept for compat; quota always uses inbound+outbound
	ResetDay       int           `json:"reset_day"`      // 1..28, or 31 = last day; 0 = never
	ExpiresAt      *time.Time    `json:"expires_at,omitempty"`
	Status         ShareStatus   `json:"status"`
	TemplateID     *int64        `json:"template_id,omitempty"`
	ConnlogEnabled bool          `json:"connlog_enabled"`
	Notes          string        `json:"notes"`
	SubscriptionID *int64        `json:"subscription_id,omitempty"`
	PeriodStart    time.Time     `json:"period_start"`
	UsedUpload     int64         `json:"used_upload"`
	UsedDownload   int64         `json:"used_download"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

const (
	BillingSum  = "sum"
	BillingDual = "dual"
	BillingUp   = "up"
	BillingDown = "down"
	BillingMax  = "max"
)

// NormalizeResetDay keeps 1–28 as-is; 29/30/31 all mean last day of month (31).
func NormalizeResetDay(n int) int {
	if n <= 0 {
		return 0
	}
	if n >= 29 {
		return 31
	}
	return n
}

// NormalizeBilling accepts legacy mode names and always stores dual.
// Quota is inbound + outbound; the selector is gone.
func NormalizeBilling(mode string) (string, bool) {
	switch mode {
	case "", BillingDual, BillingSum, BillingUp, BillingDown, BillingMax:
		return BillingDual, true
	default:
		return "", false
	}
}

// Inbound is NIC rx (into the VPS). Stored as the "up" column.
func Inbound(up, _ int64) int64 { return up }

// Outbound is NIC tx (out of the VPS). Stored as the "down" column.
func Outbound(_, down int64) int64 { return down }

// Total is inbound + outbound.
func Total(up, down int64) int64 { return up + down }

// OneWay is outbound (legacy name).
func OneWay(up, down int64) int64 { return Outbound(up, down) }

// TwoWay is inbound + outbound (legacy name).
func TwoWay(up, down int64) int64 { return Total(up, down) }

// Billed is always inbound + outbound. mode is ignored.
func Billed(_ string, up, down int64) int64 { return Total(up, down) }

// TrafficSample is a raw cumulative counter reading (retained ~48h).
type TrafficSample struct {
	ServerID int64     `json:"server_id"`
	NodeID   *int64    `json:"node_id,omitempty"`
	TS       time.Time `json:"ts"`
	RxBytes  int64     `json:"rx_bytes"`
	TxBytes  int64     `json:"tx_bytes"`
}

// TrafficBucket is an aggregated traffic row (hourly or daily). Exactly one
// of the subject ids is set; up/down are deltas within the bucket.
type TrafficBucket struct {
	Bucket        time.Time `json:"bucket"`
	ServerID      *int64    `json:"server_id,omitempty"`
	NodeID        *int64    `json:"node_id,omitempty"`
	ExternalSubID *int64    `json:"external_sub_id,omitempty"`
	ShareID       *int64    `json:"share_id,omitempty"`
	Up            int64     `json:"up"`
	Down          int64     `json:"down"`
}

// DesiredStateStatus tracks delivery of one revision.
type DesiredStateStatus string

const (
	DesiredPending DesiredStateStatus = "pending"
	DesiredApplied DesiredStateStatus = "applied"
	DesiredFailed  DesiredStateStatus = "failed"
	DesiredStale   DesiredStateStatus = "superseded"
)

// DesiredState is one signed-off revision of what a server must run.
type DesiredState struct {
	ID        int64              `json:"id"`
	ServerID  int64              `json:"server_id"`
	Revision  int64              `json:"revision"`
	Payload   json.RawMessage    `json:"payload"`
	Hash      string             `json:"hash"`
	Status    DesiredStateStatus `json:"status"`
	Error     string             `json:"error"`
	CreatedAt time.Time          `json:"created_at"`
	AppliedAt *time.Time         `json:"applied_at,omitempty"`
}

// AuditEvent records a control-plane mutation.
type AuditEvent struct {
	ID       int64           `json:"id"`
	TS       time.Time       `json:"ts"`
	UserID   *int64          `json:"user_id,omitempty"`
	Username string          `json:"username"`
	Action   string          `json:"action"`
	Target   string          `json:"target"`
	Detail   json.RawMessage `json:"detail,omitempty"`
	IP       string          `json:"ip"`
}

// AccessLog records one subscription fetch.
type AccessLog struct {
	ID             int64     `json:"id"`
	SubscriptionID int64     `json:"subscription_id"`
	TS             time.Time `json:"ts"`
	IP             string    `json:"ip"`
	UserAgent      string    `json:"user_agent"`
	Format         string    `json:"format"`
	Status         int       `json:"status"`
}

// BanRule blocks subscription access by IP / CIDR / UA pattern.
type BanRule struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"` // ip|cidr|ua|rate
	Value     string     `json:"value"`
	Reason    string     `json:"reason"`
	Enabled   bool       `json:"enabled"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// ConnEvent is one proxied connection observed on an agent.
type ConnEvent struct {
	ID       int64     `json:"id"`
	ServerID int64     `json:"server_id"`
	NodeID   int64     `json:"node_id"`
	ShareID  *int64    `json:"share_id,omitempty"`
	TS       time.Time `json:"ts"`
	Network  string    `json:"network"` // tcp|udp
	DestHost string    `json:"dest_host"`
	DestPort int       `json:"dest_port"`
	SrcHost  string    `json:"src_host,omitempty"`
	AgentSeq int64     `json:"agent_seq"`
}

// Settings keys.
const (
	SettingSiteName         = "site.name"
	SettingSiteURL          = "site.url"
	SettingShortLinks       = "subscription.short_links"
	SettingUserinfoDefault  = "subscription.userinfo_default"
	SettingTelegramToken    = "telegram.bot_token"
	SettingTelegramChatID   = "telegram.chat_id"
	SettingTelegramDaily    = "telegram.daily_report"
	SettingTelegramHour     = "telegram.daily_hour"
	SettingConnlogRetention = "connlog.retention_days"
	SettingAggRetention     = "connlog.aggregate_retention_days"
	SettingConnlogSelf      = "connlog.self_enabled"
	SettingSampleRetention  = "traffic.sample_retention_hours"
	SettingHourlyRetention  = "traffic.hourly_retention_days"
	SettingAccessRetention  = "access_log.retention_days"
	SettingAgentOfflineSec  = "agent.offline_after_seconds"
	SettingQuotaAlertPct    = "quota.alert_percent"
	SettingSingBoxVersion   = "core.singbox_version"
	SettingSnellVersion     = "core.snell_version"
	SettingMitaVersion      = "core.mita_version"
	SettingRateLimitPerMin  = "security.subscription_rate_per_min"
)

// Protocol identifiers (Clash/mihomo spelling).
const (
	ProtocolShadowsocks = "ss"
	ProtocolVMess       = "vmess"
	ProtocolVLESS       = "vless"
	ProtocolTrojan      = "trojan"
	ProtocolHysteria2   = "hysteria2"
	ProtocolTUIC        = "tuic"
	ProtocolAnyTLS      = "anytls"
	ProtocolSnell       = "snell"
	ProtocolMieru       = "mieru"
	ProtocolWireGuard   = "wireguard"
	ProtocolSocks5      = "socks5"
	ProtocolHTTP        = "http"
	ProtocolShadowTLS   = "shadowtls"
)

// DeployableProtocols lists what ctlvps can create on a managed VPS.
var DeployableProtocols = []string{
	ProtocolVLESS, ProtocolAnyTLS, ProtocolHysteria2, ProtocolTUIC,
	ProtocolTrojan, ProtocolShadowsocks, ProtocolSnell, ProtocolMieru, ProtocolWireGuard,
}

// CoreFor returns the core that serves protocol p. Snell is only implemented
// by the official snell-server; everything else runs in sing-box.
func CoreFor(p string, _ CoreMode) Core {
	if p == ProtocolMieru {
		return CoreMita
	}
	if p == ProtocolSnell {
		return CoreSnell
	}
	return CoreSingBox
}

// ProtocolAllowed reports whether p may be deployed on a server in mode.
// "lean" servers run sing-box only, so Snell is unavailable there.
func ProtocolAllowed(p string, mode CoreMode) bool {
	if mode == CoreModeLean && (p == ProtocolSnell || p == ProtocolMieru) {
		return false
	}
	for _, d := range DeployableProtocols {
		if d == p {
			return true
		}
	}
	return false
}
