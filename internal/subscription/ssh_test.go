package subscription

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/proxynode"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
	"gopkg.in/yaml.v3"
)

func sshFixtureKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestSSHClientImportAndRender(t *testing.T) {
	host, key := sshFixtureKey(t)
	pin := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey())))
	p := proxynode.Proxy{Name: "SSH fixture", Type: "ssh", Server: "ssh.example.test", Port: 22, Params: map[string]any{"username": "forward", "private-key": key, "host-key": []string{pin}}}
	raw, err := yaml.Marshal(map[string]any{"proxies": []any{p.ClashMap()}})
	if err != nil {
		t.Fatal(err)
	}
	parsed := proxynode.ParseAny(string(raw))
	if len(parsed.Proxies) != 1 || len(parsed.Errors) != 0 || parsed.Proxies[0].Str("private-key") != key {
		t.Fatal("SSH inline key did not survive import")
	}
	out, ok := SingBoxOutbound(parsed.Proxies[0], "upstream")
	if !ok || out["user"] != "forward" || out["private_key"] != key || out["detour"] != "upstream" || out["host_key"].([]string)[0] != pin {
		t.Fatal("SSH outbound fields missing")
	}
	b := &Bundle{Name: "fixture", Proxies: parsed.Proxies}
	rendered, err := RenderMihomo(b)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip := proxynode.ParseAny(string(rendered.Body))
	if len(roundtrip.Proxies) != 1 || roundtrip.Proxies[0].Str("private-key") != key {
		t.Fatal("mihomo SSH output lost authentication")
	}
	links, err := RenderRaw(b, true)
	if err != nil || strings.TrimSpace(string(links.Body)) != "" {
		t.Fatal("SSH must not invent a share URI")
	}
	for name, change := range map[string]func(map[string]any){
		"missing pin":     func(m map[string]any) { delete(m, "host-key") },
		"fingerprint":     func(m map[string]any) { m["host-key"] = []string{ssh.FingerprintSHA256(host.PublicKey())} },
		"duplicate pin":   func(m map[string]any) { m["host-key"] = []string{pin, pin} },
		"key path":        func(m map[string]any) { m["private-key"] = "/tmp/client-key" },
		"udp":             func(m map[string]any) { m["udp"] = true },
		"skip validation": func(m map[string]any) { m["skip-cert-verify"] = true },
		"two credentials": func(m map[string]any) { m["password"] = "test-only" },
	} {
		t.Run(name, func(t *testing.T) {
			m := p.ClashMap()
			change(m)
			if _, err := proxynode.FromClashMap(m); err == nil {
				t.Fatal("invalid SSH configuration accepted")
			}
		})
	}
	other := proxynode.Proxy{Name: "other", Type: "ss", Server: "example.test", Port: 1, Params: map[string]any{"password": "line1\nline2"}}
	if err := proxynode.Validate(other); err == nil {
		t.Fatal("SSH inline key exception weakened other protocols")
	}
}

// Opt-in runtime check against a pre-verified official core. The fixture only
// provides direct-tcpip to its own echo listener; it has no shell or filesystem.
func TestSSHClientRuntime(t *testing.T) {
	binary := os.Getenv("CTLVPS_SSH_TEST_SINGBOX")
	if binary == "" {
		t.Skip("set CTLVPS_SSH_TEST_SINGBOX to a verified sing-box 1.12.14")
	}
	host, _ := sshFixtureKey(t)
	client, clientKey := sshFixtureKey(t)
	wrong, _ := sshFixtureKey(t)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	sshd, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sshd.Close()
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "forward" && string(pass) == "fixture-only" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == "forward" && bytes.Equal(key.Marshal(), client.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	config.AddHostKey(host)
	go func() {
		for {
			c, err := sshd.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				conn, channels, requests, err := ssh.NewServerConn(c, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					var target struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if incoming.ChannelType() != "direct-tcpip" || ssh.Unmarshal(incoming.ExtraData(), &target) != nil || net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))) != echo.Addr().String() {
						incoming.Reject(ssh.Prohibited, "fixture target only")
						continue
					}
					upstream, err := net.DialTimeout("tcp", echo.Addr().String(), time.Second)
					if err != nil {
						incoming.Reject(ssh.ConnectionFailed, "unavailable")
						continue
					}
					ch, requests, err := incoming.Accept()
					if err != nil {
						upstream.Close()
						continue
					}
					go ssh.DiscardRequests(requests)
					go func() { defer ch.Close(); defer upstream.Close(); go io.Copy(upstream, ch); io.Copy(ch, upstream) }()
				}
			}()
		}
	}()
	for _, mode := range []string{"password", "private-key", "wrong-host-key"} {
		t.Run(mode, func(t *testing.T) {
			pin := host.PublicKey()
			if mode == "wrong-host-key" {
				pin = wrong.PublicKey()
			}
			params := map[string]any{"username": "forward", "host-key": []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pin)))}}
			if mode == "private-key" {
				params["private-key"] = clientKey
			} else {
				params["password"] = "fixture-only"
			}
			p := proxynode.Proxy{Name: "ssh-fixture", Type: "ssh", Server: "127.0.0.1", Port: sshd.Addr().(*net.TCPAddr).Port, Params: params}
			out, ok := SingBoxOutbound(p, "")
			if !ok {
				t.Fatal("compile failed")
			}
			reserve, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := reserve.Addr().String()
			port := reserve.Addr().(*net.TCPAddr).Port
			reserve.Close()
			doc := map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "socks", "listen": "127.0.0.1", "listen_port": port}}, "outbounds": []any{out}, "route": map[string]any{"final": "ssh-fixture"}}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			cmd := exec.Command(binary, "run", "-c", path)
			cmd.Stdout = &log
			cmd.Stderr = &log
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			ready := false
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if err == nil {
					c.Close()
					ready = true
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if !ready {
				t.Fatal("core did not listen; fixture credentials withheld")
			}
			dialer, err := proxy.SOCKS5("tcp", addr, nil, &net.Dialer{Timeout: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			c, err := dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", echo.Addr().String())
			if c != nil {
				defer c.Close()
			}
			if mode == "wrong-host-key" {
				if err == nil {
					t.Fatal("incorrect host key was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal("SSH connection failed; fixture credentials withheld")
			}
			c.SetDeadline(time.Now().Add(2 * time.Second))
			message := []byte("ctlvps-ssh-echo")
			if _, err = c.Write(message); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(message))
			if _, err = io.ReadFull(c, got); err != nil || !bytes.Equal(got, message) {
				t.Fatal("SSH echo failed")
			}
		})
	}
}
