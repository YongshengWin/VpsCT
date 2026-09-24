//go:build linux

package main

import (
	"context"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/store"
	"ctlvps/internal/wgconfig"
	"encoding/json"
	"fmt"
)

func testWGAccess(ctx context.Context, st *store.Store, d *desired.Builder, server domain.Server, admin *fixtureAdmin) {
	var n domain.Node
	admin.request("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", server.ID), map[string]any{"protocol": "wireguard", "name": "WG access fixture", "port": 25111}, 201, &n)
	waitApplied := func() {
		rec, e := st.LatestDesiredState(ctx, server.ID)
		must(e)
		eventually("WG access apply", func() bool {
			a, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && a.ApplyError == "" && a.AppliedRevision == rec.Revision
		})
	}
	waitApplied()
	start := func(node domain.Node) func() {
		var params map[string]any
		must(json.Unmarshal(node.Params, &params))
		c, e := wgconfig.Decode(params)
		must(e)
		endpoint := c.Endpoint("192.0.2.1", node.ListenPort)
		endpoint["tag"] = "wg-client"
		writeJSON("/tmp/wg-access-client.json", map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "socks", "listen": "127.0.0.1", "listen_port": 1080}}, "endpoints": []any{endpoint}, "route": map[string]any{"final": "wg-client"}})
		return process(ctx, "ip", "netns", "exec", "landing", "/opt/ctlvps/bin/sing-box", "run", "-c", "/tmp/wg-access-client.json")
	}
	stop := start(n)
	eventually("WG client TCP", func() bool { got, e := probe(0, "tcp", "203.0.113.10"); return e == nil && got == "192.0.2.1" })
	for _, transport := range []string{"udp", "greeting"} {
		got, e := probe(0, transport, "203.0.113.10")
		if e != nil || got != "192.0.2.1" {
			stop()
			panic(fmt.Sprintf("WG %s business failed: %v", transport, e))
		}
	}
	eventually("WG node accounting", func() bool {
		var rx, tx int64
		e := st.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(up),0),COALESCE(SUM(down),0) FROM traffic_daily WHERE subject='node' AND subject_id=?`, n.ID).Scan(&rx, &tx)
		return e == nil && rx > 0 && tx > 0
	})
	run("ip", "netns", "exec", "landing", "ip", "addr", "add", "192.168.50.2/32", "dev", "lo")
	if _, e := probe(0, "tcp", "192.168.50.2"); e == nil {
		stop()
		panic("WG private business escaped")
	}
	admin.request("POST", fmt.Sprintf("/api/v1/nodes/%d/regenerate", n.ID), nil, 200, &n)
	waitApplied()
	if _, e := probe(0, "tcp", "203.0.113.10"); e == nil {
		stop()
		panic("old WG peer survived rotation")
	}
	stop()
	stop = start(n)
	eventually("new WG peer", func() bool { got, e := probe(0, "tcp", "203.0.113.10"); return e == nil && got == "192.0.2.1" })
	admin.request("DELETE", fmt.Sprintf("/api/v1/nodes/%d", n.ID), nil, 204, nil)
	waitApplied()
	if _, e := probe(0, "tcp", "203.0.113.10"); e == nil {
		stop()
		panic("WG peer survived delete")
	}
	stop()
	fmt.Println("PASS WireGuard access API -> real agent/systemd -> TCP/UDP/server-first -> accounting -> private deny -> rotation -> delete")
}
