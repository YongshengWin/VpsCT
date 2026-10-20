package agent

import (
	"context"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/networkguard"
	"ctlvps/internal/proxyguard"
	"ctlvps/internal/udpsession"
)

func (a *Agent) udpStatus(status agentproto.ForwardUDPStatus) {
	a.mu.Lock()
	a.udpSessions = status
	a.mu.Unlock()
}

// Independent of controller I/O. Only this worker renews the separate UDP lease;
// missing health automatically fences UDP, including after agent termination.
func (a *Agent) udpSessionLoop(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var reaper udpsession.Reaper
	lastPID, failures := 0, 0
	lastAddress, lastSecret := "", ""
	var lastRenew time.Time
	reset := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		started := time.Now()
		call, cancel := context.WithTimeout(ctx, 4*time.Second)
		pid, cfg, api, err := core.LoadUDPManager(call, a.Systemd)
		code := "udp_core_unavailable"
		if err == nil && api == nil {
			reaper = udpsession.Reaper{Reaped: reaper.Reaped}
			reset = true
			a.udpStatus(agentproto.ForwardUDPStatus{State: "inactive", UpdatedAt: time.Now(), Reaped: reaper.Reaped})
			cancel()
			continue
		}
		if err == nil {
			if pid != lastPID || api.Address != lastAddress || cfg.Secret != lastSecret {
				reset = true
				reaper = udpsession.Reaper{Reaped: reaper.Reaped}
			}
			lastPID, lastAddress, lastSecret = pid, api.Address, cfg.Secret
			if time.Since(lastRenew) >= networkguard.Lease {
				reset = true
			}
			if reset {
				code = "udp_api_unavailable"
				err = reaper.Reset(call, api, cfg)
			}
			if err == nil {
				var live map[udpsession.Flow]bool
				code = "udp_conntrack_unavailable"
				live, err = udpsession.LiveFor(call, reaper.ObservedFlows())
				if err == nil {
					code = "udp_api_unavailable"
					err = reaper.Sweep(call, api, cfg, live, time.Now())
				}
			}
			if err == nil {
				code = "udp_guard_unavailable"
				err = proxyguard.ForwardUDPLease(call, cfg.Policies, true)
			}
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		status := agentproto.ForwardUDPStatus{State: "healthy", UpdatedAt: time.Now(), LastHealthy: lastRenew, Reaped: reaper.Reaped, Active: reaper.Active, SweepMillis: time.Since(started).Milliseconds()}
		if err == nil {
			failures = 0
			reset = false
			lastRenew = time.Now()
			status.LastHealthy = lastRenew
		} else {
			failures++
			status.State, status.Code = "degraded", code
			if failures >= 3 {
				// Revocation does not alter general readiness, TCP, or tunnel sockets.
				revoke, done := context.WithTimeout(ctx, time.Second)
				e := proxyguard.ForwardUDPLease(revoke, cfg.Policies, false)
				done()
				if e == nil || time.Since(lastRenew) >= networkguard.Lease {
					status.State = "paused"
					reset = true
				}
			}
		}
		a.udpStatus(status)
		total := 0
		for _, n := range reaper.Active {
			total += n
		}
		period := 500 * time.Millisecond
		if total >= 1024 {
			period = time.Second
		}
		tick.Reset(period)
	}
}
