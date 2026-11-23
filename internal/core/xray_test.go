package core

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/nft"
	"ctlvps/internal/nodecombo"
	"ctlvps/internal/provision"
	"ctlvps/internal/proxynode"
	"ctlvps/internal/subscription"
)

func xrayFixture(t *testing.T) (*agentproto.DesiredState, agentproto.NodeSpec) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := map[string]any{"network": "xhttp", "xhttp_path": "/transport-test", "xhttp_mode": "stream-one", "xhttp_host": "origin.example.test", "uuid": "00000000-0000-4000-8000-000000000001", "reality_private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "reality_public_key": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "reality_short_id": "0123456789abcdef", "handshake_server": "origin.example.test", "handshake_port": 443}
	ds := &agentproto.DesiredState{Revision: 1, ServerID: 1, XrayVersion: 1, CoreMode: "stable", IPv4Only: true, Versions: map[string]agentproto.CoreVersion{"xray": agentproto.PinnedXray()}}
	return ds, agentproto.NodeSpec{NodeID: 1, Core: "xray", Protocol: "vless", ListenPort: 20771, Params: p}
}
func TestXrayClosedProfile(t *testing.T) {
	ds, n := xrayFixture(t)
	b, err := XrayConfig(ds, n)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"target":"origin.example.test:443"`)) || bytes.Contains(b, []byte("flow")) || bytes.Contains(b, []byte("api")) {
		t.Fatal("unexpected Xray configuration")
	}
	for _, f := range []func(*agentproto.DesiredState, *agentproto.NodeSpec){func(d *agentproto.DesiredState, n *agentproto.NodeSpec) { d.CoreMode = "lean" }, func(d *agentproto.DesiredState, n *agentproto.NodeSpec) { d.XrayVersion = 0 }, func(d *agentproto.DesiredState, n *agentproto.NodeSpec) {
		d.Versions["xray"].SHA256["amd64"] = strings.Repeat("a", 64)
	}, func(d *agentproto.DesiredState, n *agentproto.NodeSpec) {
		v := d.Versions["xray"]
		v.URL = "https://example.test/asset.zip"
		d.Versions["xray"] = v
	}, func(d *agentproto.DesiredState, n *agentproto.NodeSpec) { n.Params["flow"] = "xtls-rprx-vision" }, func(d *agentproto.DesiredState, n *agentproto.NodeSpec) {
		n.Params["extra"] = map[string]any{"headers": true}
	}, func(d *agentproto.DesiredState, n *agentproto.NodeSpec) { n.Params["reality_public_key"] = "wrong" }, func(d *agentproto.DesiredState, n *agentproto.NodeSpec) { n.Core = "singbox" }, func(d *agentproto.DesiredState, n *agentproto.NodeSpec) { n.Network = &agentproto.NodeNetworkSpec{} }} {
		d, n := xrayFixture(t)
		f(d, &n)
		d.Nodes = []agentproto.NodeSpec{n}
		d.Hash = agentproto.ContentHash(d)
		if agentproto.ValidateDesired(d, 1, 0, "") == nil {
			t.Fatal("unsafe Xray desired state accepted")
		}
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		if XrayPlatform() {
			t.Fatal("advertised unsupported platform")
		}
		if _, err := NewXray(Paths{}, nil).EnsureInstalled(context.Background(), agentproto.PinnedXray()); err == nil {
			t.Fatal("platform check bypassed")
		}
	}
}

// Opt-in only inside a --network none Linux AMD64 container. Mount the reviewed
// archive read-only, add origin.example.test=127.0.0.2, and allow unprivileged
// low ports. No VPS, controller, public resolver, production keys or internet.
func TestOfficialXrayXHTTPTransport(t *testing.T) {
	archive := os.Getenv("VPSCT_TEST_XRAY_ARCHIVE")
	if archive == "" {
		t.Skip("requires isolated Linux AMD64 official archive fixture")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("requires Linux AMD64")
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sum, err := FileDigest(f)
	if err != nil || sum != agentproto.PinnedXray().SHA256["amd64"] {
		t.Fatal("untrusted fixture archive")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "xray")
	file, err := os.OpenFile(bin, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if err := extractBinaryTo(f, "xray", file); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "Xray "+agentproto.PinnedXray().Version+" ") {
		t.Fatal("wrong official binary")
	}
	camouflage := httptest.NewUnstartedServer(http.NotFoundHandler())
	camouflage.Listener.Close()
	camouflage.Listener, err = net.Listen("tcp", "127.0.0.2:443")
	if err != nil {
		t.Fatal(err)
	}
	camouflage.TLS = &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
	camouflage.Config.ErrorLog = log.New(io.Discard, "", 0)
	camouflage.EnableHTTP2 = true
	camouflage.StartTLS()
	defer camouflage.Close()
	payload := []byte("isolated-round-trip")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !bytes.Equal(b, payload) {
			http.Error(w, "unexpected body", 400)
			return
		}
		w.Write(payload)
	}))
	defer target.Close()
	start := func(t *testing.T, config []byte, name string) func() {
		t.Helper()
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, config, 0600); err != nil {
			t.Fatal(err)
		}
		if err := exec.CommandContext(ctx, bin, "run", "-test", "-config", path).Run(); err != nil {
			t.Fatalf("%s config rejected", name)
		}
		cmd := exec.CommandContext(ctx, bin, "run", "-config", path)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return func() { cmd.Process.Kill(); cmd.Wait() }
	}
	request := func(t *testing.T, link *url.URL, path, id string, want bool) {
		t.Helper()
		q := link.Query()
		port, e := strconv.Atoi(link.Port())
		if e != nil {
			t.Fatal("invalid exported listener port")
		}
		client := map[string]any{
			"log":      map[string]string{"loglevel": "none"},
			"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": 20772, "protocol": "socks", "settings": map[string]string{"auth": "noauth"}}},
			"outbounds": []any{map[string]any{
				"protocol":       link.Scheme,
				"settings":       map[string]any{"vnext": []any{map[string]any{"address": link.Hostname(), "port": port, "users": []any{map[string]string{"id": id, "encryption": q.Get("encryption")}}}}},
				"streamSettings": map[string]any{"security": q.Get("security"), "network": q.Get("type"), "xhttpSettings": map[string]any{"path": path, "mode": q.Get("mode"), "host": q.Get("host")}, "realitySettings": map[string]string{"fingerprint": q.Get("fp"), "serverName": q.Get("sni"), "publicKey": q.Get("pbk"), "shortId": q.Get("sid")}},
			}}}
		cfg, _ := json.Marshal(client)
		stop := start(t, cfg, "client")
		defer stop()
		proxy, _ := url.Parse("socks5://127.0.0.1:20772")
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		h := http.Client{Transport: transport, Timeout: 2 * time.Second}
		for deadline := time.Now().Add(3 * time.Second); ; {
			conn, e := net.DialTimeout("tcp", "127.0.0.1:20772", 100*time.Millisecond)
			if e == nil {
				conn.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("client listener unavailable")
			}
			time.Sleep(50 * time.Millisecond)
		}
		resp, e := h.Post(target.URL, "application/octet-stream", bytes.NewReader(payload))
		ok := false
		if e == nil {
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			ok = err == nil && resp.StatusCode == 200 && bytes.Equal(b, payload)
		}
		if ok != want {
			t.Fatalf("transport availability = %t, want %t: %v", ok, want, e)
		}
	}
	for _, mode := range []string{"auto", "stream-one", "packet-up", "stream-up"} {
		t.Run(mode, func(t *testing.T) {
			const sni = "origin.example.test"
			const h = "front.example.test"
			const path = "/not-the-default"
			created, e := provision.NewNode(domain.Server{ID: 1, CoreMode: domain.CoreModeStable, PublicHost: "127.0.0.1"}, "127.0.0.1", provision.Options{Name: "fixture", Port: 20771, Protocol: "vless", SNI: sni, VLESS: &nodecombo.VLESS{Transport: "xhttp", Security: "reality", XHTTP: &nodecombo.XHTTP{Path: path, Mode: mode, Host: h}}})
			if e != nil {
				t.Fatal(e)
			}
			ds := &agentproto.DesiredState{XrayVersion: 1, CoreMode: "stable", IPv4Only: true, Versions: map[string]agentproto.CoreVersion{"xray": agentproto.PinnedXray()}}
			n := agentproto.NodeSpec{NodeID: 1, Core: string(created.Core), Protocol: created.Protocol, ListenPort: created.ListenPort}
			if json.Unmarshal(created.ServerParams, &n.Params) != nil {
				t.Fatal("invalid provisioned server parameters")
			}
			rendered, e := subscription.RenderBundle(&subscription.Bundle{Name: "fixture", Proxies: []proxynode.Proxy{proxynode.FromDomain(created)}}, subscription.FormatURIList)
			if e != nil {
				t.Fatal(e)
			}
			link, e := url.Parse(strings.TrimSpace(string(rendered.Body)))
			if e != nil {
				t.Fatal("unparseable published URI")
			}
			q := link.Query()
			if link.User == nil || link.User.Username() == "" || q.Get("type") != "xhttp" || q.Get("security") != "reality" || q.Get("sni") != sni || q.Get("host") != h || q.Get("mode") != mode || q.Get("path") != path || q.Get("pbk") == "" || q.Get("sid") == "" || strings.Contains(string(rendered.Body), n.Params["reality_private_key"].(string)) {
				t.Fatal("published URI lost an option or exposed a private parameter")
			}

			cfg, e := XrayConfig(ds, n)
			if e != nil {
				t.Fatal(e)
			}
			stop := start(t, cfg, "server")
			defer stop()
			request(t, link, q.Get("path"), link.User.Username(), true)
			if mode == "stream-one" {
				request(t, link, "/wrong", link.User.Username(), false)
				request(t, link, q.Get("path"), "00000000-0000-4000-8000-000000000099", false)
			}
		})
	}
}

// This test provisions systemd users, units and nft rules. It can run ONLY in
// a fresh, explicitly selected disposable container with an official archive.
func TestIsolatedXraySystemdRollback(t *testing.T) {
	if os.Getenv("VPSCT_TEST_XRAY_SYSTEMD") != "1" {
		t.Skip("needs a dedicated systemd container")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil || !XrayPlatform() || os.Geteuid() != 0 {
		t.Fatal("requires disposable Linux AMD64 systemd container")
	}
	d := NewXray(DefaultPaths("/var/lib/ctlvps-agent"), NewSystemd())
	if _, e := os.Lstat(d.bin()); !os.IsNotExist(e) {
		t.Fatal("requires unused binary path")
	}
	f, e := os.Open(os.Getenv("VPSCT_TEST_XRAY_ARCHIVE"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	sum, e := FileDigest(f)
	if e != nil || sum != agentproto.PinnedXray().SHA256["amd64"] {
		t.Fatal("wrong fixture archive")
	}
	if e = os.MkdirAll(d.Paths.BinDir, 0755); e != nil {
		t.Fatal(e)
	}
	b, e := os.CreateTemp(d.Paths.BinDir, ".xray-fixture-")
	if e != nil {
		t.Fatal(e)
	}
	if e = extractBinaryTo(f, "xray", b); e != nil {
		t.Fatal(e)
	}
	digest, e := FileDigest(b)
	if e != nil {
		t.Fatal(e)
	}
	if e = CommitBinary(b, d.bin()); e != nil {
		t.Fatal(e)
	}
	trusted, _ := json.Marshal(map[string]string{"Version": agentproto.PinnedXray().Version, "SHA256": digest})
	if e = os.WriteFile(d.bin()+".trusted", trusted, 0600); e != nil {
		t.Fatal(e)
	}
	ds, n := xrayFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sd := d.Systemd
	m := nft.New()
	if e = sd.EnsureProxyBudget(ctx, ds.Tuning); e != nil {
		t.Fatal(e)
	}
	prep := func(ns []agentproto.NodeSpec) {
		t.Helper()
		groups := map[int64]string{}
		for _, n := range ns {
			g, e := sd.EnsureXraySlice(ctx, n.NodeID)
			if e != nil {
				t.Fatal(e)
			}
			groups[n.NodeID] = g
		}
		if e = m.EnsureNodes(ctx, ns); e != nil {
			t.Fatal(e)
		}
		if e = m.EnsureEgress(ctx, ns, groups); e != nil {
			t.Fatal(e)
		}
	}
	ns := []agentproto.NodeSpec{n}
	prep(ns)
	if _, e := d.EnsureInstalled(ctx, agentproto.PinnedXray()); e != nil {
		t.Fatal(e)
	}
	if _, e := d.Apply(ctx, ds, ns); e != nil {
		t.Fatal(e)
	}
	if e = m.EnsureResourceIngress(ctx, ns, nil); e != nil {
		t.Fatal(e)
	}
	if e = d.FinishActivation(ctx, true); e != nil {
		t.Fatal(e)
	}
	unit := StandaloneUnit("xray", n.ListenPort)
	if !d.runningCurrent(ctx, unit) {
		t.Fatal("wrong running executable")
	}
	_, newNode := xrayFixture(t)
	newNode.NodeID = 2
	newNode.ListenPort++
	busy, e := net.Listen("tcp4", "0.0.0.0:"+strconv.Itoa(newNode.ListenPort))
	if e != nil {
		t.Fatal(e)
	}
	defer busy.Close()
	ns = append(ns, newNode)
	prep(ns)
	if _, e = d.Apply(ctx, ds, ns); e == nil {
		t.Fatal("occupied port admitted")
	}
	ids, e := d.RestoreUnchanged(ctx, ds, ns)
	if e != nil || !ids[1] || ids[2] {
		t.Fatalf("unchanged node restoration failed: %v", e)
	}
	if e = d.FinishActivation(ctx, true); e != nil {
		t.Fatal(e)
	}
	if !sd.IsActive(ctx, unit) || sd.IsActive(ctx, StandaloneUnit("xray", newNode.ListenPort)) {
		t.Fatal("rollback did not restore exactly the prior authorized listener")
	}
	ns[0].Blocked = true
	prep(ns)
	if _, e = d.Apply(ctx, ds, ns); e == nil {
		t.Fatal("occupied replacement port admitted")
	}
	ids, e = d.RestoreUnchanged(ctx, ds, ns)
	if e != nil || len(ids) != 0 || sd.IsActive(ctx, unit) {
		t.Fatal("rollback resurrected a blocked endpoint")
	}
}

func TestXrayRecoveryRestoresOnlyAuthorizedUnits(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(strconv.FormatBool(revoked), func(t *testing.T) {
			base, sd, log := recoveryFixture(t)
			bin := filepath.Join(filepath.Dir(base), "xray")
			unit := StandaloneUnit("xray", 443)
			for path, content := range map[string]string{bin: "old xray", bin + ".policy": "authorized", bin + ".trusted": "verified old release", filepath.Join(sd.UnitDir, unit): "old Xray unit"} {
				if e := os.WriteFile(path, []byte(content), 0600); e != nil {
					t.Fatal(e)
				}
			}
			stageRecoveryFixture(t, bin)
			policy := "authorized"
			if revoked {
				policy = "rotated"
			}
			ctx := context.Background()
			if e := beginCoreApply(ctx, bin, sd, policy, []string{unit}, nil); e != nil {
				t.Fatal(e)
			}
			if e := markCoreApplying(bin); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(sd.UnitDir, unit), []byte("candidate"), 0644); e != nil {
				t.Fatal(e)
			}
			if e := finishCoreActivation(ctx, bin, sd, false); e != nil {
				t.Fatal(e)
			}
			requireRecoveryContent(t, bin, "old xray")
			requireRecoveryContent(t, bin+".trusted", "verified old release")
			requireRecoveryContent(t, filepath.Join(sd.UnitDir, unit), "old Xray unit")
			history, _ := os.ReadFile(log)
			if strings.Contains(string(history), "start "+unit) == revoked {
				t.Fatal("rollback restart authorization wrong")
			}
			if strings.Contains(string(history), singboxUnit) {
				t.Fatal("Xray rollback touched sing-box")
			}
		})
	}
}
