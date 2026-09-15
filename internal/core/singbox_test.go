package core

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
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
	if len(inbounds) != 6 {
		t.Fatalf("blocked node must be omitted: got %d inbounds", len(inbounds))
	}
	if cfg["log"].(map[string]any)["level"] != "info" {
		t.Fatal("connlog-enabled node must raise log level to info")
	}
	if cfg["route"].(map[string]any)["default_domain_resolver"].(map[string]any)["strategy"] != "ipv4_only" {
		t.Fatal("ipv4_only strategy")
	}
	outs := cfg["outbounds"].([]any)
	if len(outs) != 1 {
		t.Fatalf("want one direct outbound, got %d", len(outs))
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
	out, err := exec.Command(bin, "check", "-c", path).CombinedOutput()
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
