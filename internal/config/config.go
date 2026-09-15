// Package config parses ctlvpsd settings from flags and environment.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the process configuration.
type Config struct {
	Listen         string
	DataDir        string
	SiteURL        string
	TrustProxy     bool
	LogLevel       string
	LogJSON        bool
	DevProxy       string // vite dev server, e.g. http://127.0.0.1:5173
	AgentBinDir    string // directory with ctlvps-agent-linux-{arch} for /dl/agent
	SessionTTL     time.Duration
	DisableConnlog bool
	BackupKeep     int
	OnlineGeoIP    bool
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
	fs.StringVar(&c.Listen, "listen", env("LISTEN", ":8080"), "listen address")
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
	c.SiteURL = strings.TrimRight(strings.TrimSpace(c.SiteURL), "/")
	if c.SiteURL != "" && !strings.HasPrefix(c.SiteURL, "http://") && !strings.HasPrefix(c.SiteURL, "https://") {
		return c, fmt.Errorf("site-url must start with http:// or https://")
	}
	return c, nil
}
