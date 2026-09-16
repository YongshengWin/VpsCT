// Generates test fixtures from production renderers; never connects to a VPS.
package main

import (
	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/nft"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		panic("fixture output directory required")
	}
	dir := os.Args[1]
	candidate := len(os.Args) > 2 && os.Args[2] == "candidate"
	if e := os.MkdirAll(dir, 0700); e != nil {
		panic(e)
	}
	write := func(name, body string) {
		if e := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); e != nil {
			panic(e)
		}
	}
	kinds := []string{"singbox", "snell"}
	if candidate {
		kinds = append(kinds, "private")
	}
	for _, kind := range kinds {
		extra := []string{"Restart=no"}
		if kind == "snell" {
			extra = append(extra, "Slice="+core.SnellSlice(2))
		}
		unit := core.ServiceUnit("Isolated VpsCT boundary probe", "/usr/bin/python3 /src/scripts/security-boundary-test/run.py probe "+kind, agentproto.Tuning{}, extra...)
		if candidate {
			unit = strings.ReplaceAll(unit, "/usr/bin/python3 /src/scripts/security-boundary-test/run.py probe ", "/fixtures/ctlvps-agent raw-probe ")
			unit = strings.ReplaceAll(unit, "User=root", "User=ctlvps-probe\nGroup=ctlvps-probe\nEnvironment=CTLVPS_CANDIDATE=1")
			unit = strings.ReplaceAll(unit, "CAP_NET_ADMIN ", "")
			unit = strings.ReplaceAll(unit, " -/var/lib/ctlvps-agent", "")
			if kind == "singbox" {
				unit = strings.ReplaceAll(unit, "Slice=ctlvps-proxy.slice", "Slice=ctlvps-proxy-public.slice")
			}
			if kind == "private" {
				unit = strings.ReplaceAll(unit, "Slice=ctlvps-proxy.slice", "Slice=ctlvps-proxy-private.slice")
			}
		}
		write(kind+".service", unit)
	}
	nodes := []agentproto.NodeSpec{
		{NodeID: 1, Core: "singbox"}, {NodeID: 2, Core: "snell"}, {NodeID: 3, Core: "singbox", AllowPrivate: true},
	}
	groups := map[int64]string{1: "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice", 2: "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-n2.slice"}
	groups[3] = "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-private.slice"
	if !candidate {
		nodes = nodes[:2]
	}
	if candidate {
		// Production now uses cgroups for both cores.
		groups[1] = "/ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice"
	}
	rules, e := nft.EgressRules(nodes, groups, nil)
	if e != nil {
		panic(e)
	}
	if !candidate {
		rules = strings.ReplaceAll(rules, `socket cgroupv2 level 3 "ctlvps.slice/ctlvps-proxy.slice/ctlvps-proxy-public.slice"`, "meta mark 0x43000001")
	}
	write("egress.nft", rules)
}
