package openrc

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// This is the same restricted logical unit shape the core emits. Keeping the
// fixture local avoids importing core (which uses this package as its backend).
func proxyPlanFixture(profile string, check, acme bool) (string, string) {
	account, log, unit := "ctlvps-sb", "/var/log/ctlvps/sing-box.log", "ctlvps-singbox.service"
	if profile == "private" {
		account, log, unit = "ctlvps-sp", "/var/log/ctlvps/sing-box-private.log", "ctlvps-singbox-private.service"
	}
	action := "run"
	if check {
		unit, action = "ctlvps-proxy-check-"+profile+".service", "candidate"
	}
	body := fmt.Sprintf(`[Unit]
Description=ctlvps sing-box
After=network-online.target nss-lookup.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=root
Slice=ctlvps-proxy.slice
ExecStart=/usr/local/bin/ctlvps-agent proxy-exec singbox %s %s
Restart=always
RestartSec=3
LimitNOFILE=1048576
MemoryMax=256M
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=-/var/log/ctlvps -/var/lib/ctlvps-agent
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
LogRateLimitIntervalSec=30s
LogRateLimitBurst=100
User=%s
Group=%s
SupplementaryGroups=
Slice=ctlvps-proxy-%s.slice
CapabilityBoundingSet=
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_RAW
AmbientCapabilities=
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW
ReadWritePaths=
ReadWritePaths=%s
InaccessiblePaths=-/var/lib/ctlvps-agent -/var/lib/ctlvps-security -/var/lib/ctlvps-maintenance -/run/ctlvps-maintenance
UMask=0077
RestrictNamespaces=true
ProtectKernelModules=true
ProtectKernelLogs=true
LockPersonality=true
`, action, profile, account, account, profile, log)
	if acme {
		body += fmt.Sprintf("BindPaths=/var/lib/ctlvps-agent/acme:/var/lib/ctlvps-proxy/%s/acme\nReadWritePaths=/var/lib/ctlvps-proxy/%s/acme\n", profile, profile)
	}
	if check {
		body += "Type=oneshot\nRestart=no\nSlice=system.slice\nPrivateNetwork=true\nOOMScoreAdjust=1000\n"
	} else {
		body += "Environment=GOMEMLIMIT=96MiB\nIPAccounting=yes\n"
	}
	return unit, body + "\n[Install]\nWantedBy=multi-user.target\n"
}

func TestParseProxyPlansPreserveTheirExactIsolationProfiles(t *testing.T) {
	for _, profile := range []string{"public", "private"} {
		for _, check := range []bool{false, true} {
			for _, acme := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/check=%v/acme=%v", profile, check, acme), func(t *testing.T) {
					unit, body := proxyPlanFixture(profile, check, acme)
					p, err := parsePlan(unit, body)
					if err != nil {
						t.Fatal(err)
					}
					account, log := "ctlvps-sb", "/var/log/ctlvps/sing-box.log"
					if profile == "private" {
						account, log = "ctlvps-sp", "/var/log/ctlvps/sing-box-private.log"
					}
					action, memory, goMemory := "run", 256, 96
					if check {
						action, memory, goMemory = "candidate", 96, 64
					}
					if p.name != unit || p.profile != profile || p.account != account || p.log != log || p.check != check || p.slice != "ctlvps-proxy-"+profile+".slice" || p.command != launcher+" proxy-exec singbox "+action+" "+profile || p.memory != memory || p.goMemory != goMemory || p.nofile != 1048576 || p.restart != 3 {
						t.Fatalf("isolation profile changed: %+v", p)
					}
					if acme {
						if p.acmeSource != "/var/lib/ctlvps-agent/acme" || p.acmeTarget != "/var/lib/ctlvps-proxy/"+profile+"/acme" {
							t.Fatalf("ACME storage escaped its profile: %+v", p)
						}
					} else if p.acmeSource != "" || p.acmeTarget != "" {
						t.Fatal("unrequested writable ACME storage")
					}
				})
			}
		}
	}
}

func TestProxyPlanRejectsCommandsPrivilegesAndPathsOutsideItsIdentity(t *testing.T) {
	unit, body := proxyPlanFixture("public", false, false)
	cases := map[string][2]string{
		"arbitrary command":            {"ExecStart=/usr/local/bin/ctlvps-agent proxy-exec singbox run public", "ExecStart=/bin/sh -c id"},
		"command injection":            {"ExecStart=/usr/local/bin/ctlvps-agent proxy-exec singbox run public", "ExecStart=/usr/local/bin/ctlvps-agent proxy-exec singbox run public; id"},
		"different profile":            {"ExecStart=/usr/local/bin/ctlvps-agent proxy-exec singbox run public", "ExecStart=/usr/local/bin/ctlvps-agent proxy-exec singbox run private"},
		"root account":                 {"User=ctlvps-sb\n", "User=root\n"},
		"foreign group":                {"Group=ctlvps-sb\n", "Group=wheel\n"},
		"supplementary group":          {"SupplementaryGroups=\n", "SupplementaryGroups=wheel\n"},
		"missing group reset":          {"SupplementaryGroups=\n", ""},
		"extra bounding capability":    {"CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_RAW\n", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_RAW CAP_NET_ADMIN\n"},
		"extra ambient capability":     {"AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW\n", "AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW CAP_SYS_ADMIN\n"},
		"writable management state":    {"ReadWritePaths=/var/log/ctlvps/sing-box.log\n", "ReadWritePaths=/var/lib/ctlvps-agent\n"},
		"private log":                  {"ReadWritePaths=/var/log/ctlvps/sing-box.log\n", "ReadWritePaths=/var/log/ctlvps/sing-box-private.log\n"},
		"management isolation removed": {"InaccessiblePaths=-/var/lib/ctlvps-agent -/var/lib/ctlvps-security -/var/lib/ctlvps-maintenance -/run/ctlvps-maintenance\n", "InaccessiblePaths=\n"},
		"new privileges":               {"NoNewPrivileges=true\n", "NoNewPrivileges=false\n"},
		"writable root":                {"ProtectSystem=strict\n", "ProtectSystem=false\n"},
		"shared temporary directory":   {"PrivateTmp=true\n", "PrivateTmp=false\n"},
		"mutable cgroups":              {"ProtectControlGroups=true\n", "ProtectControlGroups=false\n"},
		"namespace escape":             {"RestrictNamespaces=true\n", "RestrictNamespaces=false\n"},
		"unknown property":             {"UMask=0077\n", "UMask=0077\nExecStartPre=/bin/sh -c id\n"},
		"wrong property section":       {"[Service]\n", "[Install]\n"},
		"unknown section":              {"[Service]\n", "[Mount]\n"},
		"malformed section":            {"[Service]\n", "[[Service]]\n"},
		"foreign slice":                {"Slice=ctlvps-proxy-public.slice\n", "Slice=system.slice\n"},
		"unbounded memory":             {"MemoryMax=256M\n", "MemoryMax=infinity\n"},
		"unbounded descriptors":        {"LimitNOFILE=1048576\n", "LimitNOFILE=infinity\n"},
		"arbitrary environment":        {"Environment=GOMEMLIMIT=96MiB\n", "Environment=LD_PRELOAD=/tmp/foreign.so\n"},
		"concealed environment":        {"Environment=GOMEMLIMIT=96MiB\n", "Environment=LD_PRELOAD=/tmp/foreign.so\nEnvironment=GOMEMLIMIT=96MiB\n"},
		"concealed command":            {"ExecStart=/usr/local/bin/ctlvps-agent proxy-exec singbox run public\n", "ExecStart=/bin/sh -c id\nExecStart=/usr/local/bin/ctlvps-agent proxy-exec singbox run public\n"},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			mutated := strings.Replace(body, change[0], change[1], 1)
			if mutated == body {
				t.Fatal("mutation did not alter the fixture")
			}
			if _, err := parsePlan(unit, mutated); err == nil {
				t.Fatal("unsafe logical proxy plan accepted")
			}
		})
	}
	for _, profile := range []string{"public", "private"} {
		unit, body := proxyPlanFixture(profile, false, true)
		for _, bind := range []string{"/etc/ctlvps:/var/lib/ctlvps-proxy/" + profile + "/acme", "/var/lib/ctlvps-agent/acme:/tmp/acme", "/var/lib/ctlvps-agent/acme:/var/lib/ctlvps-proxy/other/acme"} {
			mutated := strings.Replace(body, "BindPaths=/var/lib/ctlvps-agent/acme:/var/lib/ctlvps-proxy/"+profile+"/acme", "BindPaths="+bind, 1)
			if _, err := parsePlan(unit, mutated); err == nil {
				t.Fatal("foreign ACME mount accepted", bind)
			}
		}
	}
}

func TestSecurityListsResetAndAccumulateBeforeValidation(t *testing.T) {
	unit, body := proxyPlanFixture("public", false, true)
	props, err := properties(body)
	if err != nil {
		t.Fatal(err)
	}
	if props["ReadWritePaths"] != "/var/log/ctlvps/sing-box.log /var/lib/ctlvps-proxy/public/acme" || props["CapabilityBoundingSet"] != "CAP_NET_BIND_SERVICE CAP_NET_RAW" || props["AmbientCapabilities"] != "CAP_NET_BIND_SERVICE CAP_NET_RAW" {
		t.Fatal("empty reset or subsequent additions changed", props)
	}
	for _, key := range []string{"CapabilityBoundingSet", "AmbientCapabilities", "ReadWritePaths"} {
		t.Run("missing reset "+key, func(t *testing.T) {
			mutated := strings.Replace(body, key+"=\n", "", 1)
			if _, err := parsePlan(unit, mutated); err == nil {
				t.Fatal("a security list without its reset silently discarded the original entries")
			}
		})
	}
	// Addition of a previously allowed list may not conceal an earlier entry.
	for _, key := range []string{"CapabilityBoundingSet", "AmbientCapabilities"} {
		t.Run("concealed addition "+key, func(t *testing.T) {
			mutated := strings.Replace(body, key+"=CAP_NET_BIND_SERVICE CAP_NET_RAW\n", key+"=CAP_SYS_ADMIN\n"+key+"=CAP_NET_BIND_SERVICE CAP_NET_RAW\n", 1)
			if _, err := parsePlan(unit, mutated); err == nil {
				t.Fatal("earlier privilege was concealed by a later additive property")
			}
		})
	}
	for key, extra := range map[string]string{
		"InaccessiblePaths":       "/unrelated",
		"RestrictAddressFamilies": "AF_VSOCK",
		"BindPaths":               "/etc/ctlvps:/private-config",
	} {
		t.Run("concealed addition "+key, func(t *testing.T) {
			needle := key + "="
			mutated := strings.Replace(body, needle, key+"="+extra+"\n"+needle, 1)
			if _, err := parsePlan(unit, mutated); err == nil {
				t.Fatal("earlier additive property escaped validation")
			}
		})
	}
	resetGroups := strings.Replace(body, "SupplementaryGroups=\n", "SupplementaryGroups=wheel\nSupplementaryGroups=\n", 1)
	if _, err := parsePlan(unit, resetGroups); err != nil {
		t.Fatal("explicit supplementary group reset did not clear its inherited groups", err)
	}
	if _, err := properties("[Service]\nReadWritePaths=/etc\nReadWritePaths=\nReadWritePaths=/var/log/ctlvps/sing-box.log\nReadWritePaths=/var/lib/ctlvps-proxy/public/acme\n"); err != nil {
		t.Fatal("supported list reset rejected", err)
	}
}

func TestPreflightAlwaysKeepsItsOwnIsolatedNetworkAndResourceBudget(t *testing.T) {
	for _, profile := range []string{"public", "private"} {
		unit, body := proxyPlanFixture(profile, true, false)
		for key, value := range map[string]string{
			"PrivateNetwork=true": "PrivateNetwork=false",
			"Type=oneshot":        "Type=simple",
			"Restart=no":          "Restart=always",
			"Slice=system.slice":  "Slice=ctlvps-proxy-" + profile + ".slice",
			"OOMScoreAdjust=1000": "OOMScoreAdjust=0",
		} {
			t.Run(profile+"/"+key, func(t *testing.T) {
				if _, err := parsePlan(unit, strings.Replace(body, key, value, 1)); err == nil {
					t.Fatal("preflight isolation was weakened")
				}
			})
		}
		// A check cannot inherit the business memory budget, even if a later
		// caller supplies a larger limit in an otherwise valid generated unit.
		p, err := parsePlan(unit, strings.Replace(body, "MemoryMax=256M", "MemoryMax=65536M", 1))
		if err != nil || p.memory != 96 {
			t.Fatal("preflight escaped its fixed memory ceiling", p.memory, err)
		}
		if _, err := parsePlan(unit, strings.Replace(body, " candidate "+profile, " run "+profile, 1)); err == nil {
			t.Fatal("preflight was allowed to start the active configuration")
		}
	}
}

func TestProxyPlanResourceBudgetBounds(t *testing.T) {
	unit, body := proxyPlanFixture("public", false, false)
	for _, item := range []struct {
		key, original string
		valid         []string
		invalid       []string
	}{
		{"MemoryMax", "256M", []string{"16M", "65536M"}, []string{"0M", "15M", "65537M", "256", "-1M"}},
		{"LimitNOFILE", "1048576", []string{"1024", "1048576"}, []string{"0", "1023", "1048577", "-1"}},
		{"RestartSec", "3", []string{"1", "300"}, []string{"0", "301", "-1", "3s"}},
		{"Environment", "GOMEMLIMIT=96MiB", []string{"GOMEMLIMIT=16MiB", "GOMEMLIMIT=65536MiB"}, []string{"GOMEMLIMIT=0MiB", "GOMEMLIMIT=65537MiB", "GOMEMLIMIT=64MiB LD_PRELOAD=/tmp/x"}},
	} {
		for _, value := range item.valid {
			mutated := strings.Replace(body, item.key+"="+item.original+"\n", item.key+"="+value+"\n", 1)
			if _, err := parsePlan(unit, mutated); err != nil {
				t.Fatalf("bounded %s=%s rejected: %v", item.key, value, err)
			}
		}
		for _, value := range item.invalid {
			mutated := strings.Replace(body, item.key+"="+item.original+"\n", item.key+"="+value+"\n", 1)
			if _, err := parsePlan(unit, mutated); err == nil {
				t.Fatalf("unbounded %s=%s accepted", item.key, value)
			}
		}
	}
}

func TestManagedPlansRejectDropinsAndImpersonatedUnitNames(t *testing.T) {
	_, body := proxyPlanFixture("public", false, false)
	for _, unit := range []string{"ctlvps-agent.service", "ctlvps-service-host.service", "ctlvps-proxy-guard.service", "ctlvps-singbox@30001.service", "ctlvps-snell@30001.service", "ctlvps-mita@30001.service", "ctlvps-singbox.service.d/override.conf", "ctlvps-singbox.service/../ctlvps-agent.service", "/ctlvps-singbox.service", "ctlvps-singbox.service;id"} {
		if _, err := parsePlan(unit, body); err == nil {
			t.Fatal("impersonated proxy plan accepted", unit)
		}
	}
	for unit, want := range map[string]string{
		"ctlvps-service-host.service":    groupRoot + "/ctlvps-service-host.service",
		"ctlvps-proxy.slice":             proxyGroup,
		"ctlvps-proxy-public.slice":      proxyGroup + "/ctlvps-proxy-public.slice",
		"ctlvps-proxy-private.slice":     proxyGroup + "/ctlvps-proxy-private.slice",
		"ctlvps-singbox.service":         proxyGroup + "/ctlvps-proxy-public.slice/ctlvps-singbox.service",
		"ctlvps-singbox-private.service": proxyGroup + "/ctlvps-proxy-private.slice/ctlvps-singbox-private.service",
	} {
		if got, err := cgroup(unit); err != nil || got != want {
			t.Fatal("managed cgroup identity changed", unit, got, err)
		}
	}
	for _, unit := range []string{"ctlvps-proxy-n01.slice", "ctlvps-proxy-other.slice", "ctlvps-proxy-public.slice/../ctlvps-agent.service", "ctlvps-singbox.service.d/x", "foreign.service"} {
		if _, err := cgroup(unit); err == nil {
			t.Fatal("foreign cgroup identity accepted", unit)
		}
	}
	for _, unit := range []string{"ctlvps-agent.service", "ctlvps-service-host.service", "ctlvps-proxy-guard.service", "ctlvps-singbox.service", "ctlvps-singbox-private.service"} {
		script, err := Script(unit)
		if err != nil || !strings.Contains(script, "supervisor=\"supervise-daemon\"") || !strings.Contains(script, "command_args=\"openrc-exec "+unit+"\"") || !strings.Contains(script, "openrc-verify "+unit) || !strings.Contains(script, "openrc-stop "+unit) {
			t.Fatal("service script escaped its fixed command identity", unit, err)
		}
	}
	for _, unit := range []string{"ctlvps-singbox@30001.service", "ctlvps-proxy-check-public.service", "ctlvps-proxy-public.slice", "ctlvps-singbox.service.d/x", "ctlvps-singbox.service;id"} {
		if _, err := Script(unit); err == nil {
			t.Fatal("foreign OpenRC script identity accepted", unit)
		}
	}
	if _, err := properties("[Unit]\nDescription=VpsCT proxy budget\n[Slice]\nMemoryAccounting=yes\nMemoryHigh=192M\nMemoryMax=256M\n"); err != nil {
		t.Fatal("fixed aggregate budget properties rejected", err)
	}
}

func TestAggregateProxyBudgetKeepsItsFixedHighWatermark(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup runtime budget parser is implemented only on Linux")
	}
	body := "[Unit]\nDescription=VpsCT proxy resource budget\n[Slice]\nMemoryAccounting=yes\nMemoryHigh=192M\nMemoryMax=256M\n"
	for _, max := range []int{16, 256, 65536} {
		candidate := strings.Replace(body, "MemoryHigh=192M", fmt.Sprintf("MemoryHigh=%dM", max*3/4), 1)
		candidate = strings.Replace(candidate, "MemoryMax=256M", fmt.Sprintf("MemoryMax=%dM", max), 1)
		if got, err := budget(candidate); err != nil || got != max {
			t.Fatalf("bounded aggregate budget %d rejected: %d %v", max, got, err)
		}
	}
	for name, candidate := range map[string]string{
		"disabled accounting": strings.Replace(body, "MemoryAccounting=yes", "MemoryAccounting=no", 1),
		"missing accounting":  strings.Replace(body, "MemoryAccounting=yes\n", "", 1),
		"wrong watermark":     strings.Replace(body, "MemoryHigh=192M", "MemoryHigh=256M", 1),
		"missing watermark":   strings.Replace(body, "MemoryHigh=192M\n", "", 1),
		"unbounded max":       strings.Replace(body, "MemoryMax=256M", "MemoryMax=infinity", 1),
		"below minimum":       strings.Replace(body, "MemoryMax=256M", "MemoryMax=15M", 1),
		"above maximum":       strings.Replace(body, "MemoryMax=256M", "MemoryMax=65537M", 1),
		"foreign property":    body + "Delegate=yes\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := budget(candidate); err == nil {
				t.Fatal("aggregate proxy budget constraint was bypassed")
			}
		})
	}
}

func TestMitaPlanPinsIdentityAndRequiresCompleteMeter(t *testing.T) {
	_, body := proxyPlanFixture("public", false, false)
	body = strings.ReplaceAll(body, "proxy-exec singbox run public", "proxy-exec mieru run 30101")
	body = strings.ReplaceAll(body, "ctlvps-sb", "ctlvps-mi30101")
	body = strings.ReplaceAll(body, "Slice=ctlvps-proxy-public.slice", "Slice=ctlvps-proxy-n100.slice")
	body = strings.ReplaceAll(body, "CAP_NET_BIND_SERVICE CAP_NET_RAW", "CAP_NET_BIND_SERVICE")
	body = strings.ReplaceAll(body, "ReadWritePaths=/var/log/ctlvps/sing-box.log", "ReadWritePaths=-/var/log/ctlvps/mita-30101.log")
	unit := "ctlvps-mita@30101.service"
	p, err := parsePlan(unit, body)
	if err != nil {
		t.Fatal(err)
	}
	if p.account != "ctlvps-mi30101" || p.slice != "ctlvps-proxy-n100.slice" {
		t.Fatal(p)
	}
	for _, change := range [][2]string{
		{"IPAccounting=yes", "IPAccounting=no"},
		{"ctlvps-proxy-n100.slice", "ctlvps-proxy-public.slice"},
		{"proxy-exec mieru run 30101", "proxy-exec mieru run 30102"},
		{"ctlvps-mi30101", "root"},
		{"ReadWritePaths=-/var/log/ctlvps/mita-30101.log", "ReadWritePaths=/etc"},
	} {
		if _, err := parsePlan(unit, strings.ReplaceAll(body, change[0], change[1])); err == nil {
			t.Fatalf("accepted unsafe mita plan: %v", change)
		}
	}
	for _, unit := range []string{"ctlvps-mita@030101.service", "ctlvps-mita@1024.service", "ctlvps-mita@65536.service", "ctlvps-mita@30101.service;id"} {
		if _, err := serviceName(unit); err == nil {
			t.Fatal("accepted foreign mita identity", unit)
		}
	}
	if g, err := cgroup("ctlvps-proxy-n100.slice"); err != nil || g != proxyGroup+"/ctlvps-proxy-n100.slice" {
		t.Fatal(g, err)
	}
}
