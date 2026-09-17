package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/nft"
	"ctlvps/internal/provision"
)

func specFor(t *testing.T, srv domain.Server, id int64, proto string, port int, share bool) agentproto.NodeSpec {
	t.Helper()
	n, err := provision.NewNode(srv, "", provision.Options{Protocol: proto, Port: port, Obfs: proto == "hysteria2"})
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]any{}
	_ = json.Unmarshal(n.ServerParams, &params)
	spec := agentproto.NodeSpec{NodeID: id, Name: n.Name, Protocol: proto, Core: string(n.Core), ListenPort: port, Params: params, ConnlogEnabled: share}
	if proto != "vless" && proto != "ss" && proto != "snell" {
		spec.Cert = &agentproto.CertSpec{Mode: "self_signed", Domain: "203.0.113.10"}
	}
	return spec
}

func TestSingBoxConfigPassesCheck(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("sing-box not installed")
	}
	dir := t.TempDir()
	srv := domain.Server{ID: 1, Name: "hk", PublicHost: "203.0.113.10", CoreMode: domain.CoreModeStable, CertMode: "self_signed"}
	ds := &agentproto.DesiredState{ServerID: 1, PublicHost: "203.0.113.10", IPv4Only: true, Tuning: agentproto.Tuning{GoMemLimitMB: 128}}
	ds.Nodes = []agentproto.NodeSpec{
		specFor(t, srv, 1, "vless", 20001, false),
		specFor(t, srv, 2, "anytls", 20002, true),
		specFor(t, srv, 3, "hysteria2", 20003, false),
		specFor(t, srv, 4, "tuic", 20004, false),
		specFor(t, srv, 5, "trojan", 20005, false),
		specFor(t, srv, 6, "ss", 20006, false),
		specFor(t, srv, 7, "snell", 20007, false),
	}
	blocked := specFor(t, srv, 8, "vless", 20008, false)
	blocked.Blocked = true
	ds.Nodes = append(ds.Nodes, blocked)

	paths := Paths{BinDir: dir, ConfDir: dir, LogDir: dir, CertDir: filepath.Join(dir, "certs"), DataDir: dir}
	d := NewSingBox(paths, NewSystemd())
	var sbNodes []agentproto.NodeSpec
	for _, n := range ds.Nodes {
		if n.Core == "singbox" {
			sbNodes = append(sbNodes, n)
		}
	}
	if len(sbNodes) != 7 {
		t.Fatalf("expected 7 sing-box nodes, got %d", len(sbNodes))
	}
	cfg, err := d.BuildConfig(ds, sbNodes)
	if err != nil {
		t.Fatal(err)
	}
	inbounds := cfg["inbounds"].([]any)
	if len(inbounds) != 7 {
		t.Fatalf("blocked node is enforced by the firewall without restarting other nodes: got %d inbounds", len(inbounds))
	}
	if cfg["log"].(map[string]any)["level"] != "info" {
		t.Fatal("connlog-enabled node must raise log level to info")
	}
	if cfg["route"].(map[string]any)["default_domain_resolver"].(map[string]any)["strategy"] != "ipv4_only" {
		t.Fatal("ipv4_only strategy")
	}
	outs := cfg["outbounds"].([]any)
	if len(outs) != 7 {
		t.Fatalf("want a marked direct outbound per node, got %d", len(outs))
	}
	one, err := d.BuildConfig(ds, []agentproto.NodeSpec{sbNodes[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(one["inbounds"].([]any)) != 1 {
		t.Fatal("per-port instance must have one inbound")
	}
	data, _ := json.MarshalIndent(one, "", "  ")
	path := filepath.Join(dir, "20001.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out []byte
	if runtime.GOOS == "linux" {
		out, err = exec.Command(bin, "check", "-c", path).CombinedOutput()
	}
	if err != nil {
		t.Fatalf("sing-box check failed: %v\n%s\n%s", err, out, data)
	}
	// certs generated for the TLS domain, reused on second call
	certs := d.Certs(sbNodes)
	if len(certs) != 1 || certs[0].Domain != "203.0.113.10" {
		t.Fatalf("certs: %+v", certs)
	}
	first, _ := os.ReadFile(filepath.Join(paths.CertDir, "203.0.113.10.crt"))
	if _, err := EnsureSelfSigned(paths.CertDir, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(paths.CertDir, "203.0.113.10.crt"))
	if string(first) != string(second) {
		t.Fatal("self-signed cert must be reused while valid")
	}
	// snell config
	var snellSpec agentproto.NodeSpec
	for _, n := range ds.Nodes {
		if n.Core == "snell" {
			snellSpec = n
		}
	}
	conf := Config(snellSpec, true)
	if !strings.Contains(conf, "listen = :::20007") || !strings.Contains(conf, "psk = ") || !strings.Contains(conf, "ipv6 = false") {
		t.Fatalf("snell conf:\n%s", conf)
	}
	unit := ServiceUnit("x", "/bin/true", ds.Tuning, "IPAccounting=yes")
	if !strings.Contains(unit, "MemoryMax=256M") || !strings.Contains(unit, "Restart=always") || !strings.Contains(unit, "IPAccounting=yes") {
		t.Fatalf("unit:\n%s", unit)
	}
	_ = context.Background()
}

func TestExpandURL(t *testing.T) {
	u := ExpandURL("https://x/{version}/sing-box-{version}-linux-{arch}.tar.gz", "1.12.14")
	if !strings.Contains(u, "1.12.14") || strings.Contains(u, "{") {
		t.Fatal(u)
	}
}

// This test touches systemd only inside the explicitly isolated fixture.
func TestSharedServiceLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_SYSTEMD_TEST") != "1" {
		t.Skip("disposable systemd container only")
	}
	ctx := context.Background()
	t.Setenv("TMPDIR", "/opt")
	dir := t.TempDir()
	_ = dir
	paths := DefaultPaths("/var/lib/ctlvps-agent")
	if err := os.MkdirAll(paths.BinDir, 0755); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile("/fixture-sing-box")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(paths.BinDir, "sing-box"), binary, 0755); err != nil {
		t.Fatal(err)
	}
	sd := NewSystemd()
	driver := NewSingBox(paths, sd)
	t.Cleanup(func() {
		_ = sd.StopDisable(ctx, singboxUnit)
		_ = os.Remove(filepath.Join(sd.UnitDir, singboxUnit))
		_ = sd.DaemonReload(ctx)
	})
	ds := &agentproto.DesiredState{Tuning: agentproto.Tuning{MemoryMaxMB: 256, GoMemLimitMB: 64}}
	nodes := []agentproto.NodeSpec{}
	for i := int64(1); i <= 6; i++ {
		nodes = append(nodes, agentproto.NodeSpec{NodeID: i, ListenPort: 21000 + int(i), Protocol: "ss", Core: "singbox", Params: map[string]any{"method": "aes-128-gcm", "password": "isolated-fixture"}})
	}
	if err = sd.EnsureProxyBudget(ctx, ds.Tuning); err != nil {
		t.Fatal(err)
	}
	g, e := sd.EnsureSingBoxSlice(ctx, false)
	if e != nil {
		t.Fatal(e)
	}
	groups := map[int64]string{}
	for _, n := range nodes {
		groups[n.NodeID] = g
	}
	if e = nft.New().EnsureEgress(ctx, nodes, groups); e != nil {
		t.Fatal(e)
	}
	if changed, err := driver.Apply(ctx, ds, nodes); err != nil || !changed {
		t.Fatal("first apply", changed, err)
	}
	time.Sleep(300 * time.Millisecond)
	if !sd.IsActive(ctx, singboxUnit) {
		t.Fatal("shared service exited")
	}
	before, err := sd.AccountingSnapshot(ctx, []string{singboxUnit})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := driver.Apply(ctx, ds, nodes); err != nil || changed {
		t.Fatal("idempotent apply restarted", changed, err)
	}
	nodes[0].Params["method"] = "invalid-fixture-cipher"
	if _, err = driver.Apply(ctx, ds, nodes); err == nil {
		t.Fatal("bad candidate accepted")
	}
	after, err := sd.AccountingSnapshot(ctx, []string{singboxUnit})
	if err != nil {
		t.Fatal(err)
	}
	if before[singboxUnit].Epoch != after[singboxUnit].Epoch {
		t.Fatal("invalid candidate interrupted working process")
	}
	nodes[0].Params["method"] = "aes-128-gcm"
	// A listener collision passes config validation but fails service startup.
	listener, err := net.Listen("tcp", "0.0.0.0:21999")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	nodes[0].ListenPort = 21999
	if _, err = driver.Apply(ctx, ds, nodes); err == nil {
		t.Fatal("activation failure not detected")
	}
	if !sd.IsActive(ctx, singboxUnit) {
		t.Fatal("previous service not restored")
	}
	// A finished installation survives coordinator restart even when the
	// desired configuration is unchanged. Every affected core must activate.
	nodes[0].ListenPort = 21001
	if err := markActivation(driver.bin()); err != nil {
		t.Fatal(err)
	}
	driver = NewSingBox(paths, sd)
	if changed, err := driver.Apply(ctx, ds, nodes); err != nil || !changed {
		t.Fatal("durable activation missed", changed, err)
	}
	if activationPending(driver.bin()) {
		t.Fatal("successful activation marker retained")
	}
	if changed, err := driver.Apply(ctx, ds, nodes); err != nil || changed {
		t.Fatal("activation repeated", changed, err)
	}
}

func TestSnellMeterSurvivesRestart(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CTLVPS_SYSTEMD_TEST") != "1" {
		t.Skip("disposable systemd container only")
	}
	t.Setenv("TMPDIR", "/opt")
	dir := t.TempDir()
	ctx := context.Background()
	sd := NewSystemd()
	n := agentproto.NodeSpec{NodeID: 123, ListenPort: 21998, Core: "snell"}
	unit := SnellUnit(n.ListenPort)
	t.Cleanup(func() {
		_ = sd.StopUnits(ctx, []string{unit, SnellSlice(n.NodeID)})
		_ = os.Remove(filepath.Join(sd.UnitDir, unit))
		_ = os.RemoveAll(filepath.Join(sd.UnitDir, unit+".d"))
		_ = os.Remove(filepath.Join(sd.UnitDir, SnellSlice(n.NodeID)))
		_ = sd.DaemonReload(ctx)
	})
	// The accounting boundary is a kernel cgroup; this fixture exercises it
	// without substituting an unofficial implementation of the Snell protocol.
	script := filepath.Join(dir, "echo.py")
	err := os.WriteFile(script, []byte("import socket\ns=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('127.0.0.1',21998));s.listen()\nwhile True:\n c,a=s.accept()\n with c:\n  while True:\n   d=c.recv(65536)\n   if not d:break\n   c.sendall(d)\n"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sd.WriteUnit(unit, ServiceUnit("fixture Snell accounting", "/usr/bin/python3 "+script, agentproto.Tuning{}, "IPAccounting=yes")); err != nil {
		t.Fatal(err)
	}
	if _, err = sd.EnsureSnellMeter(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err = sd.StartUnits(ctx, []string{unit}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	before, err := sd.AccountingSnapshot(ctx, []string{SnellSlice(n.NodeID)})
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", "127.0.0.1:21998")
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), 4096)
	_ = c.SetDeadline(time.Now().Add(time.Second))
	if _, err = c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if err = sd.StopUnits(ctx, []string{unit}); err != nil {
		t.Fatal(err)
	}
	stopped, err := sd.AccountingSnapshot(ctx, []string{SnellSlice(n.NodeID)})
	if err != nil {
		t.Fatal(err)
	}
	if err = sd.StartUnits(ctx, []string{unit}); err != nil {
		t.Fatal(err)
	}
	after, err := sd.AccountingSnapshot(ctx, []string{SnellSlice(n.NodeID)})
	if err != nil {
		t.Fatal(err)
	}
	key := SnellSlice(n.NodeID)
	if !before[key].Valid || stopped[key].Epoch != before[key].Epoch || after[key].Epoch != before[key].Epoch {
		t.Fatal("slice epoch changed across child restart")
	}
	if stopped[key].Rx-before[key].Rx < int64(len(payload)) || stopped[key].Tx-before[key].Tx < int64(len(payload)) {
		t.Fatal("traffic not accounted")
	}
	if after[key].Rx < stopped[key].Rx || after[key].Tx < stopped[key].Tx {
		t.Fatal("restart lost traffic")
	}
	if _, err := sd.FinalSnellReading(ctx, n.NodeID); err == nil {
		t.Fatal("populated slice accepted for final settlement")
	}
	if err := sd.StopUnits(ctx, []string{unit}); err != nil {
		t.Fatal(err)
	}
	final, err := sd.FinalSnellReading(ctx, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Rx < stopped[key].Rx || final.Tx < stopped[key].Tx {
		t.Fatal("final snapshot lost traffic")
	}
	for i := 0; i < 2; i++ {
		if err := sd.RemoveSnellMeter(ctx, n.NodeID); err != nil {
			t.Fatal(err)
		}
	}
	if sd.IsActive(ctx, key) {
		t.Fatal("retired slice still active")
	}
}
