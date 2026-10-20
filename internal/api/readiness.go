package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"ctlvps/internal/httpx"
	"ctlvps/internal/store"
)

// Deep checks are local and never contact agents, external subscriptions or
// upstream catalogs. Liveness stays available independently of readiness.
type localReadinessKey struct{}

// Capture the socket and forwarding boundary before security normalizes it.
// Reverse proxies, including untrusted loopback proxies, cannot invoke checks.
func withLocalReadinessAccess(r *http.Request) *http.Request {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	local := err == nil && net.ParseIP(host).IsLoopback()
	for name := range r.Header {
		name = strings.ToLower(name)
		if name == "forwarded" || strings.HasPrefix(name, "x-forwarded-") || name == "x-real-ip" || name == "cf-connecting-ip" {
			local = false
		}
	}
	return r.WithContext(context.WithValue(r.Context(), localReadinessKey{}, local))
}

func (a *API) controllerReadiness(w http.ResponseWriter, r *http.Request) error {
	if local, _ := r.Context().Value(localReadinessKey{}).(bool); !local {
		return httpx.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := a.checkControllerReadiness(ctx); err != nil {
		a.Logger.Warn("controller upgrade readiness failed", "err", err)
		return httpx.E(503, "controller_not_ready", "控制端数据或分发文件检查未通过")
	}
	httpx.OK(w, map[string]any{"ok": true})
	return nil
}

func (a *API) checkControllerReadiness(ctx context.Context) error {
	if err := a.Store.DB().PingContext(ctx); err != nil {
		return err
	}
	if _, err := a.Store.ListNodes(ctx, store.NodeFilter{}); err != nil {
		return err
	}
	if _, err := a.Store.ListSubscriptions(ctx); err != nil {
		return err
	}
	if _, err := a.Store.ListTemplates(ctx); err != nil {
		return err
	}
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		return err
	}
	for _, server := range servers {
		if a.Desired != nil {
			if _, err := a.Desired.Build(ctx, server); err != nil {
				return err
			}
		}
	}
	// Installers provide both architectures, even on a single-architecture host.
	if a.Config.AgentBinDir != "" {
		for _, arch := range []string{"amd64", "arm64"} {
			path := a.findAgentBinary("linux-" + arch)
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				return os.ErrInvalid
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
