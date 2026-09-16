// Command ctlvpsd is the ctlvps control plane: REST API, subscription
// endpoints, agent protocol and the embedded web UI in one binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"ctlvps/internal/agentnet"
	"ctlvps/internal/api"
	"ctlvps/internal/auth"
	"ctlvps/internal/buildinfo"
	"ctlvps/internal/config"
	"ctlvps/internal/connlog"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/geoip"
	"ctlvps/internal/maintenance"
	"ctlvps/internal/notify"
	"ctlvps/internal/safehttp"
	"ctlvps/internal/scheduler"
	"ctlvps/internal/secureupdate"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"ctlvps/internal/traffic"
	"ctlvps/web"
)

func main() {
	if ok, e := agentnet.Entry(os.Args[1:]); ok {
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	if handled, err := store.BackupEntry(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if handled, err := secureupdate.Entry(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if handled, err := maintenance.Entry(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("ctlvpsd", buildinfo.String())
		return
	}
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)
	if err := run(cfg, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.LogJSON {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	keyPath := cfg.SecretsKeyFile
	if keyPath == "" {
		keyPath = filepath.Join(cfg.DataDir, "ctlvps.db.key")
	}
	st, err := store.OpenWithKey(filepath.Join(cfg.DataDir, "ctlvps.db"), keyPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	var setupToken string
	userCount, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	tokenPath := filepath.Join(cfg.DataDir, "setup-token")
	if userCount == 0 {
		setupToken, err = auth.EnsureSetupToken(tokenPath)
		if err != nil {
			return fmt.Errorf("prepare setup token: %w", err)
		}
		logger.Info("first-run setup requires the token from this local file", "path", tokenPath)
	} else if err := os.Remove(tokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("remove spent setup token", "err", err)
	}

	var cl *connlog.Store
	if !cfg.DisableConnlog {
		cl, err = connlog.Open(filepath.Join(cfg.DataDir, "connlog.db"))
		if err != nil {
			return fmt.Errorf("open connlog: %w", err)
		}
		defer cl.Close()
	}

	subs := subscription.NewService(st)
	subs.Fetcher.Client = safehttp.New(safehttp.Options{HTTPOrigins: cfg.SubscriptionHTTPOrigins, PrivateOrigins: cfg.SubscriptionPrivateOrigins})
	if err := subs.Seed(ctx); err != nil {
		return fmt.Errorf("seed templates: %w", err)
	}
	des := desired.New(st)
	tg := notify.New(func(ctx context.Context) (string, string) {
		return st.GetSetting(ctx, domain.SettingTelegramToken, ""), st.GetSetting(ctx, domain.SettingTelegramChatID, "")
	}, logger)
	shares := share.New(st, des)
	shares.OnEvent = func(ctx context.Context, sh domain.Share, kind, detail string) {
		switch kind {
		case "status":
			tg.SendDedup(ctx, fmt.Sprintf("share:%d:%s", sh.ID, detail), time.Hour, fmt.Sprintf("👥 分享「%s」状态变更为 %s", sh.Name, detail))
		}
	}
	ing := traffic.New(st)
	sched := scheduler.New(logger)
	geo := geoip.Open(filepath.Join(cfg.DataDir, "geo"))
	geo.SetOnlineEnabled(cfg.OnlineGeoIP)
	go func() {
		if err := geo.Ensure(ctx); err != nil {
			logger.Warn("geoip database unavailable", "err", err)
			return
		}
		if geo.Ready() {
			logger.Info("geoip ready")
		}
	}()

	secure := strings.HasPrefix(cfg.SiteURL, "https://")
	a := api.New(api.Deps{
		Store: st, Connlog: cl, Geo: geo, Subs: subs, Desired: des, Shares: shares, Traffic: ing, Notify: tg, Scheduler: sched, Logger: logger,
		Static: web.Handler(cfg.DevProxy),
		Config: api.Config{SiteURL: cfg.SiteURL, TrustProxy: false, TrustedProxyCIDRs: cfg.TrustedProxyCIDRs, SecureCookies: secure, SessionTTL: cfg.SessionTTL, Version: buildinfo.String(), StartedAt: time.Now(), DataDir: cfg.DataDir, AgentBinDir: cfg.AgentBinDir, SetupToken: setupToken},
	})

	registerJobs(sched, st, cl, subs, shares, des, tg, ing, cfg, logger)
	go sched.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logger.Info("ctlvpsd listening", "addr", cfg.Listen, "version", buildinfo.String(), "data", cfg.DataDir, "site_url", cfg.SiteURL, "connlog", cl != nil)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

func registerJobs(s *scheduler.Scheduler, st *store.Store, cl *connlog.Store, subs *subscription.Service, shares *share.Manager, des *desired.Builder, tg *notify.Telegram, ing *traffic.Ingestor, cfg config.Config, logger *slog.Logger) {
	s.Add(scheduler.Job{Name: "external_sync", Interval: time.Minute, RunAtStart: true, Fn: func(ctx context.Context) error {
		for id, err := range subs.SyncDue(ctx, false) {
			if err != nil {
				logger.Warn("external sync failed", "external", id, "err", err)
			}
		}
		return nil
	}})
	s.Add(scheduler.Job{Name: "share_tick", Interval: 5 * time.Minute, RunAtStart: true, Fn: shares.Tick})
	s.Add(scheduler.Job{Name: "desired_refresh", Interval: time.Hour, RunAtStart: true, Fn: des.PublishAll})
	s.Add(scheduler.Job{Name: "retention", Interval: time.Hour, RunAtStart: true, Fn: func(ctx context.Context) error {
		sampleH := st.GetSettingInt(ctx, domain.SettingSampleRetention, 48)
		hourlyD := st.GetSettingInt(ctx, domain.SettingHourlyRetention, 14)
		accessD := st.GetSettingInt(ctx, domain.SettingAccessRetention, 30)
		var errs []error
		errs = append(errs, st.PruneTraffic(ctx, time.Duration(sampleH)*time.Hour, time.Duration(hourlyD)*24*time.Hour))
		errs = append(errs, st.PruneAccessLog(ctx, time.Duration(accessD)*24*time.Hour))
		errs = append(errs, st.PruneAudit(ctx, 180*24*time.Hour))
		errs = append(errs, st.PurgeExpiredSessions(ctx))
		errs = append(errs, st.PruneDesiredStates(ctx, 20))
		if cl != nil {
			raw := st.GetSettingInt(ctx, domain.SettingConnlogRetention, 7)
			agg := st.GetSettingInt(ctx, domain.SettingAggRetention, 90)
			errs = append(errs, cl.Prune(ctx, time.Duration(raw)*24*time.Hour, time.Duration(agg)*24*time.Hour))
		}
		return errors.Join(errs...)
	}})
	s.Add(scheduler.Job{Name: "backup", Interval: time.Hour, Fn: func(ctx context.Context) error {
		now := time.Now()
		dir := filepath.Join(cfg.DataDir, "backups")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		target := filepath.Join(dir, "ctlvps-"+now.Format("20060102")+".db.enc")
		if _, err := os.Stat(target); err == nil {
			return nil // already done today
		}
		if now.Hour() < 3 {
			return nil // run after 03:00 local
		}
		if err := st.Backup(ctx, target); err != nil {
			return err
		}
		entries, _ := filepath.Glob(filepath.Join(dir, "ctlvps-*.db.enc"))
		sort.Strings(entries)
		for len(entries) > cfg.BackupKeep {
			_ = os.Remove(entries[0])
			entries = entries[1:]
		}
		logger.Info("backup written", "file", target)
		return nil
	}})
	var lastOfflineCheck = map[int64]bool{}
	s.Add(scheduler.Job{Name: "agent_watch", Interval: time.Minute, Fn: func(ctx context.Context) error {
		agents, err := st.ListAgents(ctx)
		if err != nil {
			return err
		}
		offline := time.Duration(st.GetSettingInt(ctx, domain.SettingAgentOfflineSec, 120)) * time.Second
		for _, ag := range agents {
			if ag.TokenHash == "" || ag.LastSeenAt == nil {
				continue
			}
			isOff := time.Since(*ag.LastSeenAt) > offline
			if isOff && !lastOfflineCheck[ag.ID] {
				if srv, err := st.GetServer(ctx, ag.ServerID); err == nil && srv.Enabled {
					tg.SendDedup(ctx, fmt.Sprintf("offline:%d", ag.ID), 6*time.Hour, fmt.Sprintf("🔴 %s 的 agent 离线（最后心跳 %s）", srv.Name, ag.LastSeenAt.Local().Format("01-02 15:04")))
				}
			} else if !isOff && lastOfflineCheck[ag.ID] {
				if srv, err := st.GetServer(ctx, ag.ServerID); err == nil {
					tg.SendDedup(ctx, fmt.Sprintf("online:%d", ag.ID), time.Hour, fmt.Sprintf("🟢 %s 的 agent 已恢复在线", srv.Name))
				}
			}
			lastOfflineCheck[ag.ID] = isOff
		}
		return nil
	}})
	var lastReport string
	s.Add(scheduler.Job{Name: "daily_report", Interval: 10 * time.Minute, Fn: func(ctx context.Context) error {
		if !st.GetSettingBool(ctx, domain.SettingTelegramDaily, false) {
			return nil
		}
		now := time.Now()
		if now.Hour() != st.GetSettingInt(ctx, domain.SettingTelegramHour, 9) || lastReport == now.Format("2006-01-02") {
			return nil
		}
		servers, err := st.ListServers(ctx)
		if err != nil {
			return err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "📊 ctlvps 日报 %s\n", now.Format("2006-01-02"))
		for _, srv := range servers {
			u, err := ing.ServerUsage(ctx, srv)
			if err != nil {
				continue
			}
			line := fmt.Sprintf("• %s: ↑%s ↓%s", srv.Name, human(u.Up), human(u.Down))
			if u.Quota > 0 {
				line += fmt.Sprintf(" (%.0f%%)", u.Percent)
			}
			b.WriteString(line + "\n")
		}
		shares, _ := st.ListShares(ctx, nil)
		active, exhausted := 0, 0
		for _, sh := range shares {
			switch sh.Status {
			case domain.ShareActive:
				active++
			case domain.ShareExhausted:
				exhausted++
			}
		}
		fmt.Fprintf(&b, "分享: %d 活跃 / %d 用尽", active, exhausted)
		if err := tg.Send(ctx, b.String()); err != nil {
			return err
		}
		lastReport = now.Format("2006-01-02")
		return nil
	}})
}

func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
