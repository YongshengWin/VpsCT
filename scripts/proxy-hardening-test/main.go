// Explicit fixture integration driver; this is not shipped to users.
package main

import (
	"context"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/domain"
	"ctlvps/internal/nft"
	"ctlvps/internal/provision"
	"ctlvps/internal/proxyguard"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/proxysandbox"
	"ctlvps/internal/subscription"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func main() {
	if handled, err := proxyguard.Entry(os.Args[1:]); handled {
		if err != nil {
			panic(err)
		}
		return
	}
	if handled, e := proxysandbox.Entry(os.Args[1:]); handled {
		if e != nil {
			panic(e)
		}
		return
	}
	native := os.Getenv("CTLVPS_NATIVE_FIXTURE") == "1"
	if native {
		st, e := os.Lstat("/run/ctlvps-native-test/authorized")
		if e != nil || os.Geteuid() != 0 || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Sys().(*syscall.Stat_t).Uid != 0 {
			panic("explicit root-owned native fixture authorization required")
		}
	} else if _, e := os.Stat("/.dockerenv"); e != nil {
		panic("container required")
	}
	ctx := context.Background()
	sd := core.NewSystemd()
	paths := core.DefaultPaths("/var/lib/ctlvps-agent")
	d := core.NewSingBox(paths, sd)
	ds := &agentproto.DesiredState{PublicHost: "fixture.test", Tuning: agentproto.Tuning{MemoryMaxMB: 256, GoMemLimitMB: 64}}
	n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: "trojan", ListenPort: 443, Params: map[string]any{"password": "synthetic-password"}, Cert: &agentproto.CertSpec{Mode: "external", Domain: "fixture.test", CertPath: "/var/lib/ctlvps-agent/source.crt", KeyPath: "/var/lib/ctlvps-agent/source.key"}}
	if native {
		n.ListenPort = 24543
		n.Cert = &agentproto.CertSpec{Mode: "self_signed", Domain: "fixture.test"}
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "acme":
			n.Cert = &agentproto.CertSpec{Mode: "acme", Domain: "acme-fixture.test", Email: "fixture@example.test"}
		case "bad":
			n.Protocol = "invalid"
		case "collision":
			n.ListenPort = 21999
		case "private":
			n.AllowPrivate = true
		case "mixed":
			p := n
			p.NodeID = 3
			p.ListenPort = 444
			if native {
				p.ListenPort = 24544
			}
			p.AllowPrivate = true
			ds.Nodes = append(ds.Nodes, p)
		}
	}
	ds.Nodes = append(ds.Nodes, n)
	if len(os.Args) > 1 && os.Args[1] == "all" {
		ds.Nodes = nil
		for i, proto := range []string{"vless", "anytls", "hysteria2", "tuic", "trojan", "ss"} {
			node, e := provision.NewNode(domain.Server{ID: 1, PublicHost: "127.0.0.1", CoreMode: domain.CoreModeStable, CertMode: "self_signed"}, "", provision.Options{Protocol: proto, Port: 22001 + i, SNI: "fixture.test", Obfs: proto == "hysteria2"})
			if e != nil {
				panic(e)
			}
			params := map[string]any{}
			if e = json.Unmarshal(node.ServerParams, &params); e != nil {
				panic(e)
			}
			if proto == "vless" {
				params["handshake_port"] = float64(8443)
			}
			spec := n
			spec.NodeID = int64(i + 1)
			spec.ListenPort = 22001 + i
			spec.Protocol = proto
			spec.Params = params
			ds.Nodes = append(ds.Nodes, spec)
			out, ok := subscription.SingBoxOutbound(proxynode.FromDomain(node), "")
			if !ok {
				panic("missing client renderer")
			}
			cfg := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "socks", "listen": "127.0.0.1", "listen_port": 24001 + i}}, "outbounds": []any{out}}
			b, e := json.Marshal(cfg)
			if e != nil {
				panic(e)
			}
			if e = os.WriteFile(filepath.Join("/tmp", "client-"+proto+".json"), b, 0600); e != nil {
				panic(e)
			}
		}
	}

	if e := sd.EnsureProxyBudget(ctx, ds.Tuning); e != nil {
		panic(e)
	}
	if len(os.Args) > 1 && (os.Args[1] == "snell" || os.Args[1] == "snell-collision") {
		n := agentproto.NodeSpec{NodeID: 20, Core: "snell", Protocol: "snell", ListenPort: 24443, Params: map[string]any{"psk": "synthetic-snell-fixture", "version": "5"}}
		if os.Args[1] == "snell-collision" {
			n.Params["psk"] = "changed-synthetic-fixture"
		}
		if _, e := sd.EnsureSnellMeter(ctx, n); e != nil {
			panic(e)
		}
		g, e := sd.ControlGroup(ctx, core.SnellSlice(n.NodeID))
		if e != nil {
			panic(e)
		}
		if e = nft.New().EnsureEgress(ctx, []agentproto.NodeSpec{n}, map[int64]string{n.NodeID: g}); e != nil {
			panic(e)
		}
		changed, e := core.NewSnell(paths, sd).Apply(ctx, ds, []agentproto.NodeSpec{n})
		if e != nil {
			panic(e)
		}
		fmt.Println("changed", changed)
		return
	}
	groups := map[int64]string{}
	for _, n := range ds.Nodes {
		g, e := sd.EnsureSingBoxSlice(ctx, n.AllowPrivate)
		if e != nil {
			panic(e)
		}
		groups[n.NodeID] = g
	}
	if e := nft.New().EnsureEgress(ctx, ds.Nodes, groups); e != nil {
		panic(e)
	}
	changed, e := d.Apply(ctx, ds, ds.Nodes)
	if e != nil {
		panic(e)
	}
	fmt.Println("changed", changed)
}
