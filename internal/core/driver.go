// Package core contains the proxy-core drivers used by ctlvps-agent:
// sing-box (VLESS-Reality, AnyTLS, Hysteria2, TUIC, Trojan, SS-2022) and the
// official snell-server. Drivers are idempotent: Apply converges the host to
// the given node set and starts a core only when at least one node needs it.
package core

import (
	"context"
	"path/filepath"

	"ctlvps/internal/agentproto"
)

// Paths are the filesystem locations used by the drivers.
type Paths struct {
	BinDir  string // /opt/ctlvps/bin
	ConfDir string // /etc/ctlvps
	LogDir  string // /var/log/ctlvps
	CertDir string // /var/lib/ctlvps-agent/certs
	DataDir string // /var/lib/ctlvps-agent (acme storage etc.)
}

// DefaultPaths for a systemd host.
func DefaultPaths(stateDir string) Paths {
	return Paths{
		BinDir:  "/opt/ctlvps/bin",
		ConfDir: "/etc/ctlvps",
		LogDir:  "/var/log/ctlvps",
		CertDir: filepath.Join(stateDir, "certs"),
		DataDir: stateDir,
	}
}

// Driver manages one proxy core.
type Driver interface {
	// Name is the core identifier used in NodeSpec.Core ("singbox" | "snell").
	Name() string
	// EnsureInstalled downloads/verifies the pinned version.
	EnsureInstalled(ctx context.Context, v agentproto.CoreVersion) (changed bool, err error)
	// Apply converges config + services for the given nodes (already filtered
	// to this core). An empty slice stops the core.
	Apply(ctx context.Context, ds *agentproto.DesiredState, nodes []agentproto.NodeSpec) (changed bool, err error)
	// Status reports process health.
	Status(ctx context.Context) agentproto.CoreStatus
	// Stop stops and disables all services of the core.
	Stop(ctx context.Context) error
}

// ResourceDriver extends a shared core without representing forwards as nodes.
// Both collections must already have local fences and resolved observations.
type ResourceDriver interface {
	Driver
	ApplyResources(context.Context, *agentproto.DesiredState, []agentproto.NodeSpec, []agentproto.ForwardSpec) (bool, error)
}

// LogPath returns the sing-box log file (used by conntail).
func (p Paths) LogPath() string { return filepath.Join(p.LogDir, "sing-box.log") }
