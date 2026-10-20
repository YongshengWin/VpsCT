package openrc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Entry never accepts paths, shell commands or caller-selected service names.
func Entry(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	op := args[0]
	if op != "openrc-agent-start" && op != "openrc-empty" && op != "openrc-meter-clean" && op != "openrc-exec" && op != "openrc-stop" && op != "openrc-check" && op != "openrc-sync" && op != "openrc-script" && op != "openrc-verify" && op != "openrc-manager" && op != "openrc-ready" {
		return false, nil
	}
	if os.Geteuid() != 0 {
		return true, errors.New("OpenRC service management requires root")
	}
	if op == "openrc-agent-start" && len(args) == 1 {
		return true, StartAgent()
	}
	if op == "openrc-meter-clean" && len(args) == 1 {
		return true, RemoveAllMeters()
	}
	if op == "openrc-empty" && len(args) == 2 {
		if _, err := cgroup(args[1]); err != nil {
			return true, err
		}
		if populated(args[1]) {
			return true, errors.New("managed cgroup still populated")
		}
		return true, nil
	}
	if op == "openrc-ready" {
		if len(args) != 1 {
			return true, errors.New("unexpected OpenRC readiness argument")
		}
		return true, AgentReady()
	}
	if op == "openrc-manager" {
		if len(args) != 1 {
			return true, errors.New("unexpected service-host argument")
		}
		return true, managerLoop()
	}
	if op == "openrc-check" || op == "openrc-sync" {
		if len(args) != 1 {
			return true, errors.New("unexpected OpenRC argument")
		}
		if op == "openrc-sync" {
			return true, Sync()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return true, CheckEnvironment(ctx)
	}
	if len(args) != 2 {
		return true, errors.New("missing fixed OpenRC service identity")
	}
	switch op {
	case "openrc-script":
		script, e := Script(args[1])
		if e == nil {
			_, e = fmt.Print(script)
		}
		return true, e
	case "openrc-verify":
		return true, Verify(args[1])
	case "openrc-stop":
		return true, Stop(args[1])
	case "openrc-exec":
		return true, Exec(args[1])
	}
	return true, errors.New("invalid OpenRC operation")
}
