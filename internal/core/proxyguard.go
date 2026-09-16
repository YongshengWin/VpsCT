package core

import "context"

// A local timer is independent of agent polling and controller availability.
func (s *Systemd) EnsureProxyGuard(ctx context.Context, launcher string) error {
	service := "[Unit]\nDescription=VpsCT proxy firewall integrity\n[Service]\nType=oneshot\nExecStart=" + launcher + " proxy-guard\nTimeoutStartSec=45s\nUser=root\nNoNewPrivileges=true\nProtectSystem=strict\nReadWritePaths=/run/ctlvps-proxy\nProtectHome=true\nPrivateTmp=true\nProtectKernelTunables=true\nProtectControlGroups=true\nCapabilityBoundingSet=CAP_NET_ADMIN\nRestrictAddressFamilies=AF_UNIX AF_NETLINK\n"
	timer := "[Unit]\nDescription=VpsCT local proxy firewall checks\n[Timer]\nOnBootSec=5s\nOnUnitActiveSec=5s\nAccuracySec=1s\nUnit=ctlvps-proxy-guard.service\n[Install]\nWantedBy=timers.target\n"
	a, e := s.WriteUnit("ctlvps-proxy-guard.service", service)
	if e != nil {
		return e
	}
	b, e := s.WriteUnit("ctlvps-proxy-guard.timer", timer)
	if e != nil {
		return e
	}
	if a || b {
		if e = s.DaemonReload(ctx); e != nil {
			return e
		}
	}
	_, e = s.ctl(ctx, "enable", "--now", "ctlvps-proxy-guard.timer")
	return e
}
