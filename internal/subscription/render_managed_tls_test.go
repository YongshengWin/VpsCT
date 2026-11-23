package subscription

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/core"
	"ctlvps/internal/domain"
	"ctlvps/internal/nodecombo"
	"ctlvps/internal/provision"
	"ctlvps/internal/proxynode"
	"gopkg.in/yaml.v3"
)

const fixtureSNI = "cert.example.test"
const fixtureHost = "route.example.test"
const fixturePath = "/fixture/ws"
const fixtureService = "Fixture.Transport"

func managedTLSFixture(t *testing.T, proto, network, certMode string, port int) domain.Node {
	t.Helper()
	s := domain.Server{ID: 1, Name: "fixture", PublicHost: "192.0.2.10", CoreMode: domain.CoreModeStable, CertMode: certMode}
	o := provision.Options{Name: "managed-fixture", Protocol: proto, Port: port, Domain: fixtureSNI}
	if certMode == "external" {
		o.CertID = "fixture-cert"
	}
	var ws *nodecombo.WS
	var grpc *nodecombo.GRPC
	switch network {
	case "ws":
		ws = &nodecombo.WS{Path: fixturePath, Host: fixtureHost}
	case "grpc":
		grpc = &nodecombo.GRPC{ServiceName: fixtureService}
	}
	if proto == "vless" {
		o.VLESS = &nodecombo.VLESS{Transport: network, Security: "tls", WS: ws, GRPC: grpc}
	} else {
		o.Trojan = &nodecombo.Trojan{Transport: network, Security: "tls", WS: ws, GRPC: grpc}
	}
	n, err := provision.NewNode(s, "", o)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Inspect the actual wire format rather than round-tripping it through the
// same parser alone (an encoder and decoder can agree on the wrong spelling).
func TestManagedTLSFormatMatrix(t *testing.T) {
	for _, proto := range []string{"vless", "trojan"} {
		for _, network := range []string{"tcp", "ws", "grpc"} {
			for _, mode := range []string{"external", "self_signed"} {
				t.Run(proto+"/"+network+"/"+mode, func(t *testing.T) {
					p := proxynode.FromDomain(managedTLSFixture(t, proto, network, mode, 24443))
					b := &Bundle{Proxies: []proxynode.Proxy{p}}
					insecure := mode == "self_signed"
					for _, target := range append(append([]string{}, KnownFormats...), ProfileSurgeVLESS) {
						f, profile := target, ""
						if target == ProfileSurgeVLESS {
							f, profile = FormatSurge, target
						}
						r, err := RenderBundleWithProfile(b, f, profile)
						if (f == FormatSurge && ((proto == "vless" && profile == "") || (proto == "trojan" && network == "grpc"))) || (f == FormatShadowrocket && network == "grpc") {
							if !errors.Is(err, ErrNoRenderPath) || r != nil {
								t.Fatalf("%s: expected no path, got %v", target, err)
							}
							continue
						}
						if err != nil {
							t.Fatalf("%s: %v", target, err)
						}
						switch f {
						case FormatSingBox:
							out := singOutbound(t, r, p.Name)
							tls := out["tls"].(map[string]any)
							if tls["enabled"] != true || tls["server_name"] != fixtureSNI || (tls["insecure"] == true) != insecure || out["flow"] != nil || tls["reality"] != nil {
								t.Fatal("sing-box changed TLS/flow")
							}
							tr, _ := out["transport"].(map[string]any)
							if (network == "tcp" && tr != nil) || (network != "tcp" && tr["type"] != network) || network == "ws" && (tr["path"] != fixturePath || tr["headers"].(map[string]any)["Host"] != fixtureHost) || network == "grpc" && tr["service_name"] != fixtureService {
								t.Fatal("sing-box transport changed")
							}
						case FormatMihomo:
							var doc struct{ Proxies []map[string]any }
							if err := yaml.Unmarshal(r.Body, &doc); err != nil {
								t.Fatal(err)
							}
							n, err := proxynode.FromClashMap(doc.Proxies[0])
							if err != nil {
								t.Fatal(err)
							}
							checkTLSFixture(t, n, p, network, insecure)
						case FormatRaw, FormatURIList:
							body := r.Body
							if f == FormatRaw {
								body, err = base64.StdEncoding.DecodeString(string(body))
								if err != nil {
									t.Fatal(err)
								}
							}
							link := strings.TrimSpace(string(body))
							u, err := url.Parse(link)
							if err != nil {
								t.Fatal(err)
							}
							q := u.Query()
							if q.Get("security") != "tls" || q.Get("type") != network || q.Get("flow") != "" || q.Get("sni") != fixtureSNI || (q.Get("allowInsecure") == "1") != insecure || network == "ws" && (q.Get("host") != fixtureHost || q.Get("path") != fixturePath) || network == "grpc" && q.Get("serviceName") != fixtureService {
								t.Fatalf("%s changed handshake", target)
							}
							n, err := proxynode.ParseURI(link)
							if err != nil {
								t.Fatal(err)
							}
							checkTLSFixture(t, n, p, network, insecure)
						default:
							var line string
							for _, l := range strings.Split(string(r.Body), "\n") {
								if strings.HasPrefix(l, p.Name+" = ") {
									line = l
									break
								}
							}
							if line == "" {
								t.Fatalf("%s missing policy", target)
							}
							n, err := proxynode.ParseSurgeLine(line)
							if err != nil {
								t.Fatalf("%s parse: %v", target, err)
							}
							checkTLSFixture(t, n, p, network, insecure)
							if network == "ws" {
								path, host := "ws-path=", "ws-headers=Host:"
								if f == FormatShadowrocket && proto == "vless" {
									path, host = "obfs-uri=", "obfs-host="
								}
								if !strings.Contains(line, path+fixturePath) || !strings.Contains(line, host+fixtureHost) {
									t.Fatalf("%s missing WS policy fields", target)
								}
							}
							if network == "grpc" && (!strings.Contains(line, "grpc=true") || !strings.Contains(line, "grpc-service-name="+fixtureService)) {
								t.Fatal("missing modern Surge gRPC options")
							}
						}
					}
				})
			}
		}
	}
}
func checkTLSFixture(t *testing.T, n, p proxynode.Proxy, network string, insecure bool) {
	t.Helper()
	if n.Type != p.Type || n.Str("uuid") != p.Str("uuid") || n.Str("password") != p.Str("password") || n.Str("flow") != "" || n.Sub("reality-opts") != nil || nonempty(n.Str("sni"), n.Str("servername")) != fixtureSNI || n.Bool("skip-cert-verify") != insecure || n.Type == "vless" && !n.Bool("tls") || nonempty(n.Str("network"), "tcp") != network {
		t.Fatal("native or link round-trip changed handshake")
	}
	if network == "ws" {
		path, host, ok := n.BasicWebSocket()
		if !ok || path != fixturePath || host != fixtureHost {
			t.Fatal("WS identity changed")
		}
	}
	if network == "grpc" {
		s, ok := n.BasicGRPCService()
		if !ok || s != fixtureService {
			t.Fatal("gRPC service changed")
		}
	}
}

// The offline checker parses this file only; do not load it into a running
// Surge instance or touch its profile, credentials, routing, or system proxy.
func TestManagedTrojanWSLegacySurgeOffline(t *testing.T) {
	bin, err := exec.LookPath("surge-cli")
	if err != nil {
		bin = "/Applications/Surge.app/Contents/Applications/surge-cli"
		if _, e := os.Stat(bin); os.IsNotExist(e) {
			t.Skip("Surge offline checker not installed")
		} else if e != nil {
			t.Fatal(e)
		}
	}
	p := proxynode.FromDomain(managedTLSFixture(t, "trojan", "ws", "external", 24443))
	r := renderOne(t, FormatSurge, p)
	path := filepath.Join(t.TempDir(), "fixture.conf")
	if err := os.WriteFile(path, r.Body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, e := exec.CommandContext(ctx, bin, "--check", path).CombinedOutput(); e != nil {
		t.Fatalf("offline Surge check failed: %v %s", e, out)
	}
}

func writeFixtureJSON(t *testing.T, cfg any) string {
	t.Helper()
	b, e := json.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "profile.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	return path
}

// Use the real provisioner, stock core's generated inbound, and the complete
// subscription. Only test plumbing is substituted: loopback listen, ephemeral
// fixture cert and a route restricted to a single local HTTP destination. No
// Linux mark, internet target, manager, systemd or privileges are needed.
func TestManagedTLSLoopback(t *testing.T) {
	binary, e := exec.LookPath("sing-box")
	if e != nil {
		t.Skip("installed sing-box required for loopback")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture" {
			io.WriteString(w, "managed-fixture-ok")
			return
		}
		http.NotFound(w, r)
	}))
	defer origin.Close()
	cert, e := core.EnsureSelfSigned(t.TempDir(), fixtureSNI)
	if e != nil {
		t.Fatal(e)
	}
	tpl, _ := json.Marshal(map[string]any{"certificate": map[string]any{"certificate_path": cert.Cert}})
	for _, proto := range []string{"vless", "trojan"} {
		for _, network := range []string{"tcp", "ws", "grpc"} {
			t.Run(proto+"/"+network, func(t *testing.T) {
				l, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				port := l.Addr().(*net.TCPAddr).Port
				l.Close()
				n := managedTLSFixture(t, proto, network, "external", port)
				p := proxynode.FromDomain(n)
				p.Server = "127.0.0.1"
				var params map[string]any
				if err := json.Unmarshal(n.ServerParams, &params); err != nil {
					t.Fatal(err)
				}
				ds := &agentproto.DesiredState{TLSTransportVersion: agentproto.TLSTransportVersion, Versions: map[string]agentproto.CoreVersion{"sing-box": {Version: "1.13.14"}}}
				cfg, err := core.NewSingBox(core.Paths{}, nil).BuildConfig(ds, []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", Protocol: proto, ListenPort: port, Params: params, Cert: &agentproto.CertSpec{Mode: "external", Domain: fixtureSNI, ID: "fixture-cert", CertPath: cert.Cert, KeyPath: cert.Key}}})
				if err != nil {
					t.Fatal(err)
				}
				in := cfg["inbounds"].([]any)[0].(map[string]any)
				in["listen"] = "127.0.0.1"
				serverConfig := map[string]any{"inbounds": []any{in}, "outbounds": []any{map[string]any{"type": "direct", "tag": "local"}}, "route": map[string]any{"rules": []any{map[string]any{"ip_cidr": []string{"127.0.0.1/32"}, "port": origin.Listener.Addr().(*net.TCPAddr).Port, "action": "route", "outbound": "local"}, map[string]any{"action": "reject"}}}}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "run", "-c", writeFixtureJSON(t, serverConfig))
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { cancel(); _ = cmd.Wait() }()
				addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
				deadline := time.Now().Add(3 * time.Second)
				for {
					c, err := net.DialTimeout("tcp4", addr, 100*time.Millisecond)
					if err == nil {
						c.Close()
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("fixture inbound did not start")
					}
					time.Sleep(20 * time.Millisecond)
				}
				if network == "ws" {
					// sing-box does not filter inbound WS Host. A fixture virtual host
					// checks HTTP Host independently of SNI. The generated stock
					// TLS+WS inbound, not the virtual host, must check the path.
					leaf, err := tls.LoadX509KeyPair(cert.Cert, cert.Key)
					if err != nil {
						t.Fatal(err)
					}
					pool := x509.NewCertPool()
					der, err := x509.ParseCertificate(leaf.Certificate[0])
					if err != nil {
						t.Fatal(err)
					}
					pool.AddCert(der)
					proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "https", Host: addr})
					transport := &http.Transport{TLSClientConfig: &tls.Config{ServerName: fixtureSNI, RootCAs: pool}}
					defer transport.CloseIdleConnections()
					proxy.Transport = transport
					front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Host != fixtureHost || r.TLS.ServerName != fixtureSNI {
							http.NotFound(w, r)
							return
						}
						proxy.ServeHTTP(w, r)
					}))
					front.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
					front.StartTLS()
					defer front.Close()
					p.Port = front.Listener.Addr().(*net.TCPAddr).Port
				}
				fetch := func(p proxynode.Proxy, want bool) {
					t.Helper()
					b := &Bundle{Proxies: []proxynode.Proxy{p}, Template: &domain.RuleTemplate{Kind: FormatSingBox, Content: string(tpl)}}
					r, err := RenderBundle(b, FormatSingBox)
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(t.TempDir(), "client.json")
					if err = os.WriteFile(path, r.Body, 0600); err != nil {
						t.Fatal(err)
					}
					timed, stop := context.WithTimeout(ctx, 6*time.Second)
					defer stop()
					out, err := exec.CommandContext(timed, binary, "tools", "fetch", "-c", path, "-o", p.Name, origin.URL+"/fixture").CombinedOutput()
					if timed.Err() != nil || ((err == nil && strings.Contains(string(out), "managed-fixture-ok")) != want) {
						t.Fatalf("unexpected fixture fetch (want=%t): %v %s", want, err, out)
					}
				}
				fetch(p, true)
				bad := p
				bad.Params = maps.Clone(p.Params)
				if proto == "vless" {
					bad.Params["servername"] = "wrong.example.test"
				} else {
					bad.Params["sni"] = "wrong.example.test"
				}
				fetch(bad, false)
				bad.Params = maps.Clone(p.Params)
				switch network {
				case "ws":
					bad.Params["ws-opts"] = map[string]any{"path": fixturePath, "headers": map[string]any{"Host": "wrong.example.test"}}
					fetch(bad, false)
					bad.Params["ws-opts"] = map[string]any{"path": "/wrong", "headers": map[string]any{"Host": fixtureHost}}
				case "grpc":
					bad.Params["grpc-opts"] = map[string]any{"grpc-service-name": "Wrong.Service"}
				}
				if network != "tcp" {
					fetch(bad, false)
				}
				bad.Params = maps.Clone(p.Params)
				if proto == "vless" {
					bad.Params["uuid"] = "11111111-1111-4111-8111-111111111111"
				} else {
					bad.Params["password"] = "wrong-fixture"
				}
				fetch(bad, false)
			})
		}
	}
}
