// Package config parses ctlvpsd settings from flags and environment.
package config

import (
	"flag"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the process configuration.
type Config struct {
	SecretsKeyFile             string
	Listen                     string
	DataDir                    string
	SiteURL                    string
	TrustProxy                 bool
	TrustedProxyCIDRs          []string
	SubscriptionHTTPOrigins    []string
	SubscriptionPrivateOrigins []string
	LogLevel                   string
	LogJSON                    bool
	DevProxy                   string // vite dev server, e.g. http://127.0.0.1:5173
	AgentBinDir                string // directory with ctlvps-agent-linux-{arch} for /dl/agent
	SessionTTL                 time.Duration
	DisableConnlog             bool
	BackupKeep                 int
	OnlineGeoIP                bool
}

func env(key, def string) string {
	if v, ok := os.LookupEnv("CTLVPS_" + key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := env(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// Parse reads flags (which override CTLVPS_* environment variables).
func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("ctlvpsd", flag.ContinueOnError)
	var c Config
	fs.StringVar(&c.SecretsKeyFile, "secrets-key-file", env("SECRETS_KEY_FILE", ""), "data encryption key kept separately from database backups")
	var proxies, privateOrigins, httpOrigins string
	fs.StringVar(&proxies, "trusted-proxies", env("TRUSTED_PROXIES", ""), "comma-separated adjacent trusted proxy CIDRs")
	fs.StringVar(&privateOrigins, "subscription-private-origins", env("SUBSCRIPTION_PRIVATE_ORIGINS", ""), "local exact-origin exceptions for private subscription sources")
	fs.StringVar(&httpOrigins, "subscription-http-origins", env("SUBSCRIPTION_HTTP_ORIGINS", ""), "exact-origin exceptions for legacy HTTP subscription sources")
	fs.StringVar(&c.Listen, "listen", env("LISTEN", "127.0.0.1:8080"), "listen address")
	fs.StringVar(&c.DataDir, "data", env("DATA_DIR", "./data"), "data directory (sqlite, backups)")
	fs.StringVar(&c.SiteURL, "site-url", env("SITE_URL", ""), "public base URL used in subscription links")
	fs.BoolVar(&c.TrustProxy, "trust-proxy", envBool("TRUST_PROXY", false), "trust X-Forwarded-For / X-Real-IP")
	fs.StringVar(&c.LogLevel, "log-level", env("LOG_LEVEL", "info"), "debug|info|warn|error")
	fs.BoolVar(&c.LogJSON, "log-json", envBool("LOG_JSON", false), "JSON log output")
	fs.StringVar(&c.DevProxy, "dev-proxy", env("DEV_PROXY", ""), "proxy non-API requests to a Vite dev server")
	fs.StringVar(&c.AgentBinDir, "agent-bin-dir", env("AGENT_BIN_DIR", ""), "directory containing ctlvps-agent-linux-{amd64,arm64}")
	fs.DurationVar(&c.SessionTTL, "session-ttl", 30*24*time.Hour, "login session lifetime")
	fs.BoolVar(&c.DisableConnlog, "disable-connlog", envBool("DISABLE_CONNLOG", false), "do not open connlog.db / reject connection logs")
	fs.BoolVar(&c.OnlineGeoIP, "online-geoip", envBool("ONLINE_GEOIP", false), "opt in to sending public client IPs to third-party geolocation services")
	fs.IntVar(&c.BackupKeep, "backup-keep", 7, "daily sqlite backups to keep")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if proxies != "" {
		c.TrustedProxyCIDRs = strings.Split(proxies, ",")
	} else if c.TrustProxy {
		c.TrustedProxyCIDRs = []string{"127.0.0.0/8", "::1/128"}
	}
	for _, raw := range c.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return c, fmt.Errorf("invalid trusted proxy CIDR")
		}
	}
	if privateOrigins != "" {
		c.SubscriptionPrivateOrigins = strings.Split(privateOrigins, ",")
	}
	if httpOrigins != "" {
		c.SubscriptionHTTPOrigins = strings.Split(httpOrigins, ",")
	}
	for _, raw := range append(append([]string{}, c.SubscriptionHTTPOrigins...), c.SubscriptionPrivateOrigins...) {
		u, e := url.Parse(raw)
		if e != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("subscription exceptions must be exact HTTP(S) origins")
		}
	}
	c.SiteURL = strings.TrimRight(strings.TrimSpace(c.SiteURL), "/")
	if c.SiteURL != "" && !strings.HasPrefix(c.SiteURL, "http://") && !strings.HasPrefix(c.SiteURL, "https://") {
		return c, fmt.Errorf("site-url must start with http:// or https://")
	}
	if c.SiteURL != "" {
		u, e := url.Parse(c.SiteURL)
		if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("invalid site-url")
		}
		if u.Scheme == "http" && u.Hostname() != "localhost" {
			ip := net.ParseIP(u.Hostname())
			if ip == nil || !ip.IsLoopback() {
				return c, fmt.Errorf("public site-url requires HTTPS")
			}
		}
	} else {
		h, _, e := net.SplitHostPort(c.Listen)
		ip := net.ParseIP(h)
		if c.DevProxy == "" && (e != nil || ip == nil || !ip.IsLoopback()) {
			return c, fmt.Errorf("public listener requires canonical HTTPS site-url")
		}
	}
	if c.BackupKeep < 1 || c.BackupKeep > 90 {
		return c, fmt.Errorf("backup-keep must be 1..90")
	}
	return c, nil
}
