// Package proxysandbox is the fixed, unprivileged proxy exec boundary.
package proxysandbox

import (
	"ctlvps/internal/proxyguard"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Command accepts identities, never arbitrary commands or file paths.
func Command(args []string) (string, []string, error) {
	if len(args) != 3 {
		return "", nil, fmt.Errorf("invalid proxy command")
	}
	kind, action, profile := args[0], args[1], args[2]
	base := "/etc/ctlvps-proxy"
	if kind == "singbox" && (profile == "public" || profile == "private") && (action == "run" || action == "check" || action == "candidate") {
		file := "config.json"
		if action == "candidate" {
			file = "candidate.json"
			action = "check"
		}
		return "/opt/ctlvps/bin/sing-box", []string{action, "-c", filepath.Join(base, profile, file)}, nil
	}
	port, err := strconv.Atoi(profile)
	if kind == "snell" && action == "run" && err == nil && port > 0 && port <= 65535 && strconv.Itoa(port) == profile {
		return "/opt/ctlvps/bin/snell-server", []string{"-c", filepath.Join(base, "snell-"+profile, "config.conf")}, nil
	}
	return "", nil, fmt.Errorf("invalid proxy identity or action")
}

func Entry(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "proxy-exec" {
		return false, nil
	}
	bin, argv, err := Command(args[1:])
	if err != nil {
		return true, err
	}
	if os.Geteuid() == 0 {
		return true, fmt.Errorf("proxy must run as a dedicated unprivileged user")
	}
	if err = proxyguard.Ready(); err != nil {
		return true, err
	}
	return true, launch(bin, argv)
}
