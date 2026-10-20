// Command ctlvps-agent runs on each VPS: it enrols with ctlvpsd, reports
// metrics and per-port traffic, and converges sing-box / snell-server to the
// desired state. All connections are outbound.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ctlvps/internal/agent"
	"ctlvps/internal/agentnet"
	"ctlvps/internal/buildinfo"
	"ctlvps/internal/core"
	"ctlvps/internal/maintenance"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/openrc"
	"ctlvps/internal/proxyguard"
	"ctlvps/internal/proxysandbox"
	"ctlvps/internal/secureupdate"
)

func usage() {
	fmt.Fprintf(os.Stderr, `ctlvps-agent %s

usage:
  ctlvps-agent enroll --server <url> --token <enroll-token> [--state DIR]
  ctlvps-agent run [--state DIR] [--log-level LEVEL]
  ctlvps-agent status [--state DIR]
  ctlvps-agent nat-check
  ctlvps-agent version
`, buildinfo.String())
	os.Exit(2)
}

func main() {
	for _, entry := range []func([]string) (bool, error){openrc.Entry, core.InstallEntry, agent.UpdateEntry} {
		if handled, err := entry(os.Args[1:]); handled {
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	if len(os.Args) > 1 && os.Args[1] == "proxy-guard-loop" {
		if err := proxysandbox.InstallGuard(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if handled, err := proxyguard.Entry(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if handled, err := proxysandbox.Entry(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if handled, err := agentnet.Entry(os.Args[1:]); handled {
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
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "nat-check":
		cfg, err := natconfig.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "NAT 配置无效:", err)
			os.Exit(1)
		}
		ports, _ := cfg.Expand()
		fmt.Printf("NAT 配置有效：%d 条 TCP/UDP 端口映射；无映射时关闭适配\n", len(ports))
	case "capabilities":
		_ = json.NewEncoder(os.Stdout).Encode(networkconfig.CurrentCapabilities())
	case "check-update":
		fs := flag.NewFlagSet("check-update", flag.ExitOnError)
		candidate := fs.String("candidate", "", "verified candidate binary")
		state := fs.String("state", defaultState(), "state directory")
		_ = fs.Parse(os.Args[2:])
		if *candidate == "" {
			usage()
		}
		if err := secureupdate.CheckAgentCompatibility(context.Background(), *candidate, agent.StatePath(*state)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println("ctlvps-agent", buildinfo.String())
	case "enroll":
		fs := flag.NewFlagSet("enroll", flag.ExitOnError)
		server := fs.String("server", "", "ctlvpsd base URL")
		token := fs.String("token", "", "one-time enrolment token")
		state := fs.String("state", defaultState(), "state directory")
		_ = fs.Parse(os.Args[2:])
		if *server == "" || *token == "" {
			usage()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st, err := agent.Enroll(ctx, *state, *server, *token, buildinfo.Version)
		if err != nil {
			fmt.Fprintln(os.Stderr, "enroll failed:", err)
			os.Exit(1)
		}
		fmt.Printf("enrolled as server #%d (%s); state saved to %s\n", st.ServerID, st.ServerName, agent.StatePath(*state))
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		state := fs.String("state", defaultState(), "state directory")
		level := fs.String("log-level", "info", "debug|info|warn|error")
		hold := fs.Bool("hold-updates", false, "pin local canary binary; pause auto-update and web maintenance")
		_ = fs.Parse(os.Args[2:])
		logger := newLogger(*level)
		st, err := agent.LoadState(*state)
		if err != nil {
			logger.Error("load state", "err", err)
			os.Exit(1)
		}
		a := agent.New(*state, st, logger, buildinfo.Version)
		// A root policy which pins a local canary also suppresses update polling.
		a.HoldUpdates = *hold || secureupdate.Allow("agent.update") != nil
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		logger.Info("ctlvps-agent starting", "version", buildinfo.String(), "server", st.ServerURL, "server_id", st.ServerID)
		if err := a.Run(ctx); err != nil {
			logger.Error("agent exited", "err", err)
			os.Exit(1)
		}
	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		state := fs.String("state", defaultState(), "state directory")
		_ = fs.Parse(os.Args[2:])
		st, err := agent.LoadState(*state)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		a := agent.New(*state, st, newLogger("warn"), buildinfo.Version)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		fmt.Print(a.StatusSummary(ctx))
	default:
		usage()
	}
}

func defaultState() string {
	if v := os.Getenv("CTLVPS_AGENT_STATE"); v != "" {
		return v
	}
	return "/var/lib/ctlvps-agent"
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
