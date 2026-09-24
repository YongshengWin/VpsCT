//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/provision"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func testMitaSystemd(ctx context.Context, st *store.Store, d *desired.Builder, server domain.Server, admin *fixtureAdmin) {
	run("ip", "netns", "exec", "landing", "ip", "addr", "add", "192.168.50.2/32", "dev", "lo")
	copyFile("/fixtures/mita", "/opt/ctlvps/bin/mita", 0755)
	b, err := os.ReadFile("/opt/ctlvps/bin/mita")
	must(err)
	sum := sha256.Sum256(b)
	writeJSON("/opt/ctlvps/bin/mita.trusted", map[string]string{"Version": "3.37.0", "SHA256": hex.EncodeToString(sum[:])})
	waitApplied := func() {
		rec, e := st.LatestDesiredState(ctx, server.ID)
		must(e)
		eventually("mita agent apply", func() bool {
			ag, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && ag.ApplyError == "" && ag.AppliedRevision == rec.Revision
		})
	}
	for i, transport := range []string{"TCP", "UDP"} {
		if selected := os.Getenv("MITA_TEST_TRANSPORT"); selected != "" && selected != transport {
			continue
		}
		var n domain.Node
		admin.request("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", server.ID), map[string]any{"protocol": "mieru", "name": "mita fixture", "port": 23110 + i, "mieru_transport": transport}, 201, &n)
		waitApplied()
		path := fmt.Sprintf("/tmp/mieru-client-%d.json", i)
		start := func(node domain.Node) func() {
			if os.Getenv("MIERU_TEST_CLIENT") == "mihomo" {
				template := domain.RuleTemplate{Kind: "mihomo", Content: fmt.Sprintf("mixed-port: %d\nallow-lan: false\nmode: rule\nlog-level: error\nproxies: []\nproxy-groups:\n  - name: fixture\n    type: select\n    proxies: ['{{all}}']\nrules: ['MATCH,fixture']\n", 1080+i)}
				rendered, e := subscription.RenderMihomo(&subscription.Bundle{Name: "mieru-export", Template: &template, Proxies: []proxynode.Proxy{proxynode.FromDomain(node)}})
				must(e)
				must(os.WriteFile(path, rendered.Body, 0600))
				return process(ctx, "ip", "netns", "exec", "landing", "/fixtures/mihomo", "-d", fmt.Sprintf("/tmp/mihomo-%d", i), "-f", path)
			}
			var p map[string]any
			must(json.Unmarshal(node.Params, &p))
			remote := map[string]any{"ipAddress": "192.0.2.1", "portBindings": []any{map[string]any{"port": node.ListenPort, "protocol": transport}}}
			profile := map[string]any{"profileName": "fixture", "user": map[string]any{"name": p["username"], "password": p["password"]}, "servers": []any{remote}}
			writeJSON(path, map[string]any{"profiles": []any{profile}, "activeProfile": "fixture", "rpcPort": 0, "socks5Port": 1080 + i, "loggingLevel": "ERROR"})
			return process(ctx, "ip", "netns", "exec", "landing", "env", "MIERU_CONFIG_JSON_FILE="+path, "/fixtures/mieru", "run")
		}
		stop := start(n)
		eventually("mieru TCP", func() bool { got, e := probe(i, "tcp", "203.0.113.10"); return e == nil && got == "192.0.2.1" })
		if got, e := probe(i, "udp", "203.0.113.10"); e != nil || got != "192.0.2.1" {
			stop()
			panic(fmt.Sprintf("mita UDP business failed %s %v", got, e))
		}
		eventually("mita accounted by node", func() bool {
			var rx, tx int64
			e := st.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(up),0),COALESCE(SUM(down),0) FROM traffic_daily WHERE subject='node' AND subject_id=?`, n.ID).Scan(&rx, &tx)
			return e == nil && rx > 0 && tx > 0
		})
		if _, e := probe(i, "tcp", "192.168.50.2"); e == nil {
			stop()
			panic("mita reached private business")
		}
		// API responses deliberately omit ServerParams. Rotation must use the
		// stored node, as the real API does, to preserve the selected transport.
		n, err = st.GetNode(ctx, n.ID)
		must(err)
		old := n
		must(provision.RegenerateCredentials(&n, server))
		must(st.UpdateNodeCredentials(ctx, &n))
		_, _, err = d.Publish(ctx, server.ID)
		must(err)
		waitApplied()
		if _, e := probe(i, "tcp", "203.0.113.10"); e == nil {
			stop()
			panic("old mieru credentials remain valid")
		}
		stop()
		n, err = st.GetNode(ctx, n.ID)
		must(err)
		stop = start(n)
		eventually("rotated mieru credential", func() bool { got, e := probe(i, "tcp", "203.0.113.10"); return e == nil && got == "192.0.2.1" })
		admin.request("DELETE", fmt.Sprintf("/api/v1/nodes/%d", n.ID), nil, 204, nil)
		waitApplied()
		if _, e := probe(i, "tcp", "203.0.113.10"); e == nil {
			stop()
			panic("deleted mieru still reachable")
		}
		stop()
		output, _ := exec.Command("systemctl", "is-active", fmt.Sprintf("ctlvps-mita@%d.service", old.ListenPort)).CombinedOutput()
		if strings.TrimSpace(string(output)) == "active" {
			panic("mita service not cleaned")
		}
		fmt.Println("PASS mieru " + transport + " deploy -> real agent -> isolated official mita -> TCP/UDP business -> node accounting -> private deny -> rotate -> delete")
	}
}
