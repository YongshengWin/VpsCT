// Package openrc implements the fixed local Alpine service boundary. OpenRC
// supervises processes; this package owns their cgroups and proxy sandbox.
package openrc

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const UnitDir = "/etc/ctlvps/openrc"
const launcher = "/usr/local/bin/ctlvps-agent"
const groupRoot = "/ctlvps.slice"
const proxyGroup = groupRoot + "/ctlvps-proxy.slice"

type plan struct {
	name, profile, account, slice, command, log, acmeSource, acmeTarget string
	memory, nofile, restart, goMemory                                   int
	check                                                               bool
}

func mitaPort(unit string) (string, bool) {
	p := strings.TrimSuffix(strings.TrimPrefix(unit, "ctlvps-mita@"), ".service")
	n, err := strconv.Atoi(p)
	return p, err == nil && n >= 1025 && n <= 65535 && strconv.Itoa(n) == p && unit == "ctlvps-mita@"+p+".service"
}

func meterID(unit string) (string, bool) {
	id := strings.TrimSuffix(strings.TrimPrefix(unit, "ctlvps-proxy-n"), ".slice")
	n, err := strconv.ParseInt(id, 10, 64)
	return id, err == nil && n > 0 && strconv.FormatInt(n, 10) == id && unit == "ctlvps-proxy-n"+id+".slice"
}

func serviceName(unit string) (string, error) {
	if _, ok := mitaPort(unit); ok {
		return strings.TrimSuffix(unit, ".service"), nil
	}
	switch unit {
	case "ctlvps-agent.service", "ctlvps-service-host.service", "ctlvps-proxy-guard.service", "ctlvps-singbox.service", "ctlvps-singbox-private.service":
		return strings.TrimSuffix(unit, ".service"), nil
	}
	return "", errors.New("OpenRC only supports fixed agent, guard, sing-box and mita service identities")
}

func cgroup(unit string) (string, error) {
	if _, ok := meterID(unit); ok {
		return proxyGroup + "/" + unit, nil
	}
	if _, ok := mitaPort(unit); ok {
		p, err := readMitaPlan(unit)
		if err != nil {
			return "", err
		}
		return proxyGroup + "/" + p.slice + "/" + unit, nil
	}
	switch unit {
	case "ctlvps-proxy.slice":
		return proxyGroup, nil
	case "ctlvps-proxy-public.slice":
		return proxyGroup + "/" + unit, nil
	case "ctlvps-proxy-private.slice":
		return proxyGroup + "/" + unit, nil
	case "ctlvps-singbox.service":
		return proxyGroup + "/ctlvps-proxy-public.slice/" + unit, nil
	case "ctlvps-singbox-private.service":
		return proxyGroup + "/ctlvps-proxy-private.slice/" + unit, nil
	case "ctlvps-agent.service", "ctlvps-service-host.service", "ctlvps-proxy-guard.service", "ctlvps-proxy-check-public.service", "ctlvps-proxy-check-private.service":
		return groupRoot + "/" + unit, nil
	}
	return "", errors.New("unsupported OpenRC cgroup identity")
}

// Parse only internally generated logical units. ExecStart never goes through
// a shell; all identities, writable paths and capability sets are fixed here.
func parsePlan(name, body string) (plan, error) {
	p := plan{name: name, memory: 256, nofile: 1048576, restart: 3, goMemory: 64}
	switch name {
	case "ctlvps-singbox.service", "ctlvps-proxy-check-public.service":
		p.profile = "public"
		p.account = "ctlvps-sb"
		p.slice = "ctlvps-proxy-public.slice"
		p.log = "/var/log/ctlvps/sing-box.log"
	case "ctlvps-singbox-private.service", "ctlvps-proxy-check-private.service":
		p.profile = "private"
		p.account = "ctlvps-sp"
		p.slice = "ctlvps-proxy-private.slice"
		p.log = "/var/log/ctlvps/sing-box-private.log"
	default:
		port, ok := mitaPort(name)
		if !ok {
			return p, errors.New("unsupported OpenRC proxy unit")
		}
		props, err := properties(body)
		if err != nil {
			return p, err
		}
		if _, ok := meterID(props["Slice"]); !ok || props["IPAccounting"] != "yes" {
			return p, errors.New("missing mita process meter")
		}
		p.profile = "mita-" + port
		p.account = "ctlvps-mi" + port
		p.slice = props["Slice"]
		p.log = "/var/log/ctlvps/mita-" + port + ".log"
	}
	p.check = strings.HasPrefix(name, "ctlvps-proxy-check-")
	action := "run"
	if p.check {
		action = "candidate"
		p.memory = 96
	}
	p.command = launcher + " proxy-exec singbox " + action + " " + p.profile
	if port, ok := mitaPort(name); ok {
		p.command = launcher + " proxy-exec mieru run " + port
	}
	props, err := properties(body)
	if err != nil {
		return p, err
	}
	required := map[string]string{"ExecStart": p.command, "User": p.account, "Group": p.account, "SupplementaryGroups": "", "CapabilityBoundingSet": "CAP_NET_BIND_SERVICE CAP_NET_RAW", "AmbientCapabilities": "CAP_NET_BIND_SERVICE CAP_NET_RAW", "NoNewPrivileges": "true", "ProtectSystem": "strict", "ProtectHome": "true", "PrivateTmp": "true", "ProtectKernelTunables": "true", "ProtectControlGroups": "true", "RestrictSUIDSGID": "true", "RestrictNamespaces": "true", "UMask": "0077", "ProtectKernelModules": "true", "ProtectKernelLogs": "true", "LockPersonality": "true", "RestrictAddressFamilies": "AF_UNIX AF_INET AF_INET6 AF_NETLINK"}
	if _, ok := mitaPort(name); ok {
		required["CapabilityBoundingSet"] = "CAP_NET_BIND_SERVICE"
		required["AmbientCapabilities"] = "CAP_NET_BIND_SERVICE"
	}
	for k, v := range required {
		got, present := props[k]
		if !present || got != v {
			return p, fmt.Errorf("OpenRC requires %s=%s", k, v)
		}
	}
	wantSlice := p.slice
	if p.check {
		wantSlice = "system.slice"
		if props["Type"] != "oneshot" || props["Restart"] != "no" || props["PrivateNetwork"] != "true" || props["OOMScoreAdjust"] != "1000" {
			return p, errors.New("invalid OpenRC preflight isolation")
		}
	} else if props["Type"] != "simple" || props["Restart"] != "always" {
		return p, errors.New("invalid OpenRC supervision")
	}
	if props["Slice"] != wantSlice {
		return p, errors.New("invalid OpenRC proxy slice")
	}
	for _, item := range []struct {
		key      string
		dst      *int
		suffix   string
		min, max int
	}{{"MemoryMax", &p.memory, "M", 16, 65536}, {"LimitNOFILE", &p.nofile, "", 1024, 1048576}, {"RestartSec", &p.restart, "", 1, 300}} {
		v := props[item.key]
		if item.suffix != "" && !strings.HasSuffix(v, item.suffix) {
			return p, fmt.Errorf("invalid %s", item.key)
		}
		n, e := strconv.Atoi(strings.TrimSuffix(v, item.suffix))
		if e != nil || n < item.min || n > item.max {
			return p, fmt.Errorf("invalid %s", item.key)
		}
		*item.dst = n
	}
	if p.check {
		p.memory = 96
	}
	if env := props["Environment"]; env != "" {
		if !strings.HasPrefix(env, "GOMEMLIMIT=") || !strings.HasSuffix(env, "MiB") {
			return p, errors.New("unsupported proxy environment")
		}
		p.goMemory, err = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(env, "GOMEMLIMIT="), "MiB"))
		if err != nil || p.goMemory < 16 || p.goMemory > 65536 {
			return p, errors.New("invalid proxy Go memory limit")
		}
	}
	// Repeated ReadWritePaths add entries after the explicit empty reset.
	rw := strings.Fields(props["ReadWritePaths"])
	if bind := props["BindPaths"]; bind != "" {
		if _, ok := mitaPort(name); ok {
			return p, errors.New("mita does not support ACME binds")
		}
		p.acmeSource = "/var/lib/ctlvps-agent/acme"
		p.acmeTarget = "/var/lib/ctlvps-proxy/" + p.profile + "/acme"
		if bind != p.acmeSource+":"+p.acmeTarget {
			return p, errors.New("unsupported ACME bind")
		}
	}
	wantRW := p.log
	if _, ok := mitaPort(name); ok {
		wantRW = "-" + p.log
	}
	if p.acmeSource != "" {
		wantRW += " " + p.acmeTarget
	}
	if strings.Join(rw, " ") != wantRW {
		return p, errors.New("unsupported writable proxy paths")
	}
	if props["InaccessiblePaths"] != "-/var/lib/ctlvps-agent -/var/lib/ctlvps-security -/var/lib/ctlvps-maintenance -/run/ctlvps-maintenance" {
		return p, errors.New("missing management state isolation")
	}
	return p, nil
}

func properties(body string) (map[string]string, error) {
	if len(body) > 32<<10 || strings.ContainsRune(body, '\x00') {
		return nil, errors.New("invalid logical OpenRC unit")
	}
	allowed := map[string]string{
		"Description": "Unit", "After": "Unit", "Wants": "Unit", "StartLimitIntervalSec": "Unit", "WantedBy": "Install",
		"Type": "Service", "User": "Service", "Group": "Service", "SupplementaryGroups": "Service", "Slice": "Service", "ExecStart": "Service", "Restart": "Service", "RestartSec": "Service", "LimitNOFILE": "Service", "MemoryMax": "Service", "CapabilityBoundingSet": "Service", "AmbientCapabilities": "Service", "NoNewPrivileges": "Service", "ProtectSystem": "Service", "ReadWritePaths": "Service", "ProtectHome": "Service", "PrivateTmp": "Service", "ProtectKernelTunables": "Service", "ProtectControlGroups": "Service", "RestrictSUIDSGID": "Service", "RestrictAddressFamilies": "Service", "LogRateLimitIntervalSec": "Service", "LogRateLimitBurst": "Service", "InaccessiblePaths": "Service", "UMask": "Service", "RestrictNamespaces": "Service", "ProtectKernelModules": "Service", "ProtectKernelLogs": "Service", "LockPersonality": "Service", "BindPaths": "Service", "PrivateNetwork": "Service", "OOMScoreAdjust": "Service", "Environment": "Service", "IPAccounting": "Service",
		"MemoryAccounting": "Slice", "MemoryHigh": "Slice",
	}
	p := map[string]string{}
	section := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if section != "Unit" && section != "Service" && section != "Install" && section != "Slice" {
				return nil, errors.New("unknown OpenRC unit section")
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		want, known := allowed[k]
		if !ok || !known || (want != section && !(section == "Slice" && (k == "MemoryMax" || k == "IPAccounting"))) {
			return nil, fmt.Errorf("unsupported OpenRC property %s", k)
		}
		if _, present := p[k]; present && (k == "ExecStart" || k == "Environment") {
			return nil, fmt.Errorf("duplicate OpenRC property %s", k)
		}
		list := k == "ReadWritePaths" || k == "CapabilityBoundingSet" || k == "AmbientCapabilities" || k == "SupplementaryGroups" || k == "InaccessiblePaths" || k == "RestrictAddressFamilies" || k == "BindPaths"
		if list && v != "" && p[k] != "" {
			p[k] += " " + v
		} else {
			p[k] = v
		}
	}
	return p, nil
}

// Script contains no server-controlled command or shell interpolation.
func Script(unit string) (string, error) {
	name, err := serviceName(unit)
	if err != nil {
		return "", err
	}
	_, mita := mitaPort(unit)
	dependency := ""
	if mita || unit == "ctlvps-proxy-guard.service" || unit == "ctlvps-singbox.service" || unit == "ctlvps-singbox-private.service" {
		dependency = " ctlvps-service-host"
	}
	restart := 3
	if mita || unit == "ctlvps-singbox.service" || unit == "ctlvps-singbox-private.service" {
		if p, err := readUnit(unit); err == nil {
			restart = p.restart
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return fmt.Sprintf(`#!/sbin/openrc-run
# Managed by VpsCT (OpenRC v1)
description="VpsCT %s"
supervisor="supervise-daemon"
command="/usr/local/bin/ctlvps-agent"
command_args="openrc-exec %s"
pidfile="/run/%s-supervisor.pid"
respawn_delay=%d
respawn_max=0
output_logger="/usr/bin/logger -t %s"
error_logger="/usr/bin/logger -t %s"
depend() {
    need net%s
    after firewall
}
start_pre() {
    /usr/local/bin/ctlvps-agent openrc-verify %s || return 1
}
stop_post() {
    /usr/local/bin/ctlvps-agent openrc-stop %s
}
`, name, unit, name, restart, name, name, dependency, unit, unit), nil
}
