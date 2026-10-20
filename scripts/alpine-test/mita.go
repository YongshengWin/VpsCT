//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/nft"
	"ctlvps/internal/openrc"
)

func testMita(ctx context.Context, paths core.Paths) {
	must(os.MkdirAll(paths.BinDir, 0755))
	data, err := os.ReadFile("/fixtures/mita")
	must(err)
	write(paths.BinDir+"/mita", data, 0755)
	sum := sha256.Sum256(data)
	writeJSON(paths.BinDir+"/mita.trusted", map[string]string{"Version": "3.37.0", "SHA256": hex.EncodeToString(sum[:])})
	ds := &agentproto.DesiredState{ServerID: 1, Revision: 1, MitaVersion: 1, Tuning: agentproto.Tuning{MemoryMaxMB: 96, GoMemLimitMB: 64, LimitNOFILE: 8192, RestartSec: 1}}
	sd := core.NewSystemd()
	driver := core.NewMita(paths, sd)
	m := nft.New()
	must(sd.EnsureProxyBudget(ctx, ds.Tuning))
	defer driver.Stop(context.Background())
	for i, transport := range []string{"TCP", "UDP"} {
		port := 30101 + i
		node := agentproto.NodeSpec{NodeID: int64(100 + i), Core: "mieru", Protocol: "mieru", ListenPort: port, Params: map[string]any{"username": "fixture", "password": "fixture-only", "transport": transport}}
		ds.Nodes = []agentproto.NodeSpec{node}
		_, err := sd.EnsureSnellMeter(ctx, node)
		must(err)
		group, err := sd.ControlGroup(ctx, core.SnellSlice(node.NodeID))
		must(err)
		must(m.EnsureNodes(ctx, nil))
		must(m.EnsureResourceEgress(ctx, ds.Nodes, map[int64]string{node.NodeID: group}, nil, ""))
		_, err = driver.Apply(ctx, ds, ds.Nodes)
		must(err)
		must(driver.FinishActivation(ctx, true))
		pid, err := openrc.MainPID(core.MitaUnit(port))
		must(err)
		if pid <= 0 {
			panic("mita missing")
		}
		verifyProxyIsolation(pid, 0x400, core.MitaUnit(port), "/usr/local/bin/ctlvps-agent proxy-exec mieru run "+strconv.Itoa(port))
		path := "/tmp/mieru-client.json"
		profile := map[string]any{"profileName": "fixture", "user": map[string]any{"name": "fixture", "password": "fixture-only"}, "servers": []any{map[string]any{"ipAddress": "198.51.100.1", "portBindings": []any{map[string]any{"port": port, "protocol": transport}}}}}
		writeJSON(path, map[string]any{"profiles": []any{profile}, "activeProfile": "fixture", "rpcPort": 0, "socks5Port": 1080, "loggingLevel": "ERROR"})
		stop := process("ip", "netns", "exec", "alpine-peer", "env", "MIERU_CONFIG_JSON_FILE="+path, "/fixtures/mieru", "run")
		defer stop()
		eventually("mieru "+transport+" listener", func() bool { return runProbe("tcp") == nil })
		must(runProbe("udp"))
		readings, err := sd.AccountingSnapshot(ctx, []string{core.SnellSlice(node.NodeID)})
		must(err)
		before := readings[core.SnellSlice(node.NodeID)]
		if !before.Valid || before.Rx <= 0 || before.Tx <= 0 {
			panic("missing process meter")
		}
		// Both the client leg and origin leg carry this payload; a listen-port-only
		// meter cannot reach twice its size in each direction.
		payload := bytes.Repeat([]byte("mita-accounting"), 16384)
		must(exec.Command("ip", "netns", "exec", "alpine-peer", "/fixtures/integration", "mita-payload").Run())
		rx, tx, epoch, err := openrc.ReadMeter(core.SnellSlice(node.NodeID))
		must(err)
		if rx-before.Rx < int64(2*len(payload)) || tx-before.Tx < int64(2*len(payload)) {
			panic(fmt.Sprintf("incomplete origin accounting: rx=%d tx=%d payload=%d", rx-before.Rx, tx-before.Tx, len(payload)))
		}
		must(sd.EnableRestart(ctx, core.MitaUnit(port)))
		eventually("mieru restart", func() bool { return runProbe("tcp") == nil })
		rx2, tx2, epoch2, err := openrc.ReadMeter(core.SnellSlice(node.NodeID))
		must(err)
		if epoch != epoch2 || rx2 < rx || tx2 < tx {
			panic("service restart reset process meter")
		}
		// Restart helpers with an actual inode replacement, preserving mita units.
		replacement, err := os.ReadFile("/usr/local/bin/ctlvps-agent")
		must(err)
		write("/usr/local/bin/ctlvps-agent.new", replacement, 0755)
		must(os.Rename("/usr/local/bin/ctlvps-agent.new", "/usr/local/bin/ctlvps-agent"))
		must(openrc.RefreshHelpers(ctx))
		eventually("mieru helper refresh", func() bool { return runProbe("tcp") == nil })
		ds.Nodes[0].Blocked = true
		_, err = driver.Apply(ctx, ds, ds.Nodes)
		must(err)
		must(driver.FinishActivation(ctx, true))
		if runProbe("tcp") == nil {
			panic("blocked mita still forwards")
		}
		stop()
		reading, err := sd.FinalSnellReading(ctx, node.NodeID)
		must(err)
		if !reading.Valid || reading.Rx < rx2 || reading.Tx < tx2 {
			panic("final process settlement lost bytes")
		}
		must(sd.RemoveSnellMeter(ctx, node.NodeID))
		if _, _, _, err := openrc.ReadMeter(core.SnellSlice(node.NodeID)); err == nil {
			panic("retired meter retained attachment")
		}
	}
	fmt.Println("PASS Alpine official mita TCP/UDP, sandbox, full client+origin counters, service/helper restart continuity, block and final retirement")
}

func runProbe(network string) error {
	return exec.Command("ip", "netns", "exec", "alpine-peer", "/fixtures/integration", "probe-target", network, "203.0.113.10").Run()
}

func mitaPayload() {
	payload := bytes.Repeat([]byte("mita-accounting"), 16384)
	c, _, err := socks(1, "203.0.113.10")
	must(err)
	must(c.SetDeadline(time.Now().Add(20 * time.Second)))
	_, err = c.Write(payload)
	must(err)
	got := make([]byte, len(payload))
	_, err = io.ReadFull(c, got)
	must(err)
	c.Close()
	if !bytes.Equal(payload, got) {
		panic("mita payload differs")
	}
}
