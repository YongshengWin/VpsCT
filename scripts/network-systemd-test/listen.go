//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/store"
)

func testStandaloneListen(ctx context.Context, st *store.Store, d *desired.Builder, server domain.Server, interfaces map[string]string, admin *fixtureAdmin) {
	for binary, version := range map[string]string{"mita": "3.37.0", "snell-server": "5.0.1"} {
		copyFile("/fixtures/"+binary, "/opt/ctlvps/bin/"+binary, 0755)
		b, err := os.ReadFile("/opt/ctlvps/bin/" + binary)
		must(err)
		sum := sha256.Sum256(b)
		writeJSON("/opt/ctlvps/bin/"+binary+".trusted", map[string]string{"Version": version, "SHA256": hex.EncodeToString(sum[:])})
	}
	waitApplied := func() {
		must(d.ReconcileNetworkOperations(ctx))
		rec, err := st.LatestDesiredState(ctx, server.ID)
		must(err)
		eventually("standalone listener apply", func() bool {
			a, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && a.ApplyError == "" && a.AppliedRevision == rec.Revision
		})
	}
	reachable := func(host string, port int) bool {
		return exec.CommandContext(ctx, "ip", "netns", "exec", "landing", "python3", "-c", `import socket,sys; socket.create_connection((sys.argv[1],int(sys.argv[2])),1).close()`, host, strconv.Itoa(port)).Run() == nil
	}
	var nodes []domain.Node
	for i, protocol := range []string{"snell", "mieru"} {
		var n domain.Node
		admin.request("POST", fmt.Sprintf("/api/v1/servers/%d/nodes", server.ID), map[string]any{"protocol": protocol, "name": protocol + " listen fixture", "port": 25300 + i, "mieru_transport": "TCP"}, 201, &n)
		waitApplied()
		eventually(protocol+" listener before binding", func() bool { return reachable("192.0.2.1", n.ListenPort) })
		policy := &networkconfig.Node{ListenMode: "address", ListenAddress: "192.0.2.1", ListenInterfaceID: interfaces["wan0"], AdvertiseMode: "inherit", OnUnavailable: "block"}
		var view store.NetworkReadiness
		eventually(protocol+" readiness", func() bool {
			var e error
			view, e = st.ReviewNodeNetwork(ctx, n.ID, policy, nil)
			return e == nil && view.Ready
		})
		admin.request("PUT", fmt.Sprintf("/api/v1/nodes/%d/network", n.ID), map[string]any{"operation_id": fmt.Sprintf("%032x", 800+i), "expected_revision": 0, "expected_impact": view.Impact.Token, "network": policy}, 202, nil)
		waitApplied()
		if !reachable("192.0.2.1", n.ListenPort) || reachable("198.51.100.1", n.ListenPort) {
			panic(protocol + " listener address selection failed")
		}
		nodes = append(nodes, n)
	}
	n := nodes[1]
	var params map[string]any
	must(json.Unmarshal(n.Params, &params))
	path := "/tmp/mieru-listen-client.json"
	writeJSON(path, map[string]any{"profiles": []any{map[string]any{"profileName": "fixture", "user": map[string]any{"name": params["username"], "password": params["password"]}, "servers": []any{map[string]any{"ipAddress": "192.0.2.1", "portBindings": []any{map[string]any{"port": n.ListenPort, "protocol": "TCP"}}}}}}, "activeProfile": "fixture", "rpcPort": 0, "socks5Port": 1080, "loggingLevel": "ERROR"})
	stop := process(ctx, "ip", "netns", "exec", "landing", "env", "MIERU_CONFIG_JSON_FILE="+path, "/fixtures/mieru", "run")
	defer stop()
	eventually("mieru bound TCP", func() bool { got, e := probe(0, "tcp", "203.0.113.10"); return e == nil && got == "192.0.2.1" })
	if _, e := probe(0, "udp", "203.0.113.10"); e != nil {
		panic(e)
	}
	run("ip", "link", "set", "wan0", "name", "wan-renamed")
	eventually("renamed listening interface converged", func() bool {
		view, e := st.Network(ctx, server.ID)
		if e != nil || view.Snapshot == nil {
			return false
		}
		matched := false
		for _, nic := range view.Snapshot.Interfaces {
			if nic.Name == "wan-renamed" {
				if nic.ID != interfaces["wan0"] {
					panic("rename replaced stable interface identity")
				}
				matched = true
			}
		}
		if !matched {
			return false
		}
		_, e = probe(0, "tcp", "203.0.113.10")
		return e == nil
	})
	run("ip", "link", "set", "wan-renamed", "down")
	eventually("standalone missing interface fenced", func() bool {
		a, e := st.GetAgentByServer(ctx, server.ID)
		var diag struct {
			Errors map[int64]string `json:"network_binding_errors"`
		}
		_ = json.Unmarshal(a.Diagnostics, &diag)
		return e == nil && len(diag.Errors) >= 2
	})
	if _, e := probe(0, "tcp", "203.0.113.10"); e == nil {
		panic("mieru listener failed open")
	}
	fmt.Println("PASS Snell + mita actual service listening selection and unselected IP rejection; mieru TCP/UDP, rename continuity, missing interface fencing")
}
