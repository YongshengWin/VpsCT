// Command ctlvps-agent runs on each VPS: it enrols with ctlvpsd, reports
// metrics and per-port traffic, and converges sing-box / snell-server to the
// desired state. All connections are outbound.
package main

import (
	"context"
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
  ctlvps-agent version
`, buildinfo.String())
	os.Exit(2)
}

func main() {
	for _, entry := range []func([]string) (bool, error){core.InstallEntry, agent.UpdateEntry} {
		if handled, err := entry(os.Args[1:]); handled {
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
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
		a.HoldUpdates = *hold
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
