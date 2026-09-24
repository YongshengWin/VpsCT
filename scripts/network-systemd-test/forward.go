//go:build linux

package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/secureupdate"
	"ctlvps/internal/store"
	"golang.org/x/crypto/ssh"
)

func testForwardSystemd(ctx context.Context, st *store.Store, d *desired.Builder, server domain.Server, interfaces map[string]string, admin *fixtureAdmin) {
	throughSOCKS := os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-socks"
	throughSSH := os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-ssh"
	throughWG := os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-wireguard"
	throughUDP := os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-udp"
	profile := domain.EgressProfile{ServerID: server.ID, Name: "forward wan1", Kind: "direct", Enabled: true}
	raw, _ := json.Marshal(networkconfig.Direct{InterfaceID: interfaces["wan1"], Family: "dual", DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.10", Port: 15353}})
	if throughSOCKS {
		profile.Kind = "socks5"
		upstream := networkconfig.SOCKS5{Server: "192.0.2.2", ServerPort: 11080, Authentication: "none", Family: "ipv4", DNS: networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.10", Port: 15353}, Outer: networkconfig.Direct{InterfaceID: interfaces["wan0"], Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "203.0.113.10", Port: 15353}}, ConnectTimeoutSeconds: 3}
		raw, _ = json.Marshal(upstream)
		writeJSON("/tmp/fixture-forward-socks.json", map[string]any{"log": map[string]any{"level": "error"}, "inbounds": []any{map[string]any{"type": "socks", "tag": "upstream", "listen": "192.0.2.2", "listen_port": 11080}}, "outbounds": []any{map[string]any{"type": "direct", "tag": "landing-out", "inet4_bind_address": "198.51.100.2"}}, "route": map[string]any{"rules": []any{map[string]any{"inbound": []string{"upstream"}, "action": "route", "outbound": "landing-out"}}}})
		stop := process(ctx, "ip", "netns", "exec", "landing", "/opt/ctlvps/bin/sing-box", "run", "-c", "/tmp/fixture-forward-socks.json")
		defer stop()
		eventually("forward SOCKS upstream ready", func() bool {
			c, e := net.DialTimeout("tcp", "192.0.2.2:11080", 100*time.Millisecond)
			if e != nil {
				return false
			}
			c.Close()
			return true
		})
	}
	if throughSSH {
		_, host, err := ed25519.GenerateKey(rand.Reader)
		must(err)
		signer, err := ssh.NewSignerFromKey(host)
		must(err)
		der, err := x509.MarshalPKCS8PrivateKey(host)
		must(err)
		must(os.WriteFile("/tmp/fixture-ssh-host.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600))
		stop := process(ctx, "ip", "netns", "exec", "landing", "/fixtures/integration", "ssh-peer")
		defer stop()
		eventually("forward SSH peer ready", func() bool {
			c, e := net.DialTimeout("tcp", "198.51.100.2:2222", 100*time.Millisecond)
			if e != nil {
				return false
			}
			c.Close()
			return true
		})
		dns := networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.10", Port: 15353}
		transport := networkconfig.SSH{Server: "198.51.100.2", ServerPort: 2222, Authentication: "password", HostKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}, Family: "ipv4", DNS: dns, ConnectTimeoutSeconds: 2, Outer: networkconfig.Direct{InterfaceID: interfaces["wan1"], SourceIPv4: &networkconfig.Address{InterfaceID: interfaces["wan1"], Address: "198.51.100.1"}, Family: "ipv4", DNS: dns}}
		path := fmt.Sprintf("/api/v1/servers/%d/egress-profiles", server.ID)
		body := map[string]any{"expected_revision": 0, "name": "forward SSH", "kind": "ssh", "enabled": true, "config": transport, "credentials": networkconfig.SOCKS5Credentials{Username: "forward", Password: "fixture-only"}, "action": "create"}
		var review store.EgressPreview
		admin.request("POST", path+"/preview", body, 200, &review)
		if !review.Ready {
			panic("forward SSH profile preview rejected")
		}
		delete(body, "action")
		body["operation_id"], body["expected_impact"] = fmt.Sprintf("%032x", 701), review.Impact.Token
		var op store.NetworkOperation
		admin.request("POST", path, body, 202, &op)
		profile.ID, profile.Kind = op.ResourceID, "ssh"
	} else if throughWG {
		client, err := ecdh.X25519().GenerateKey(rand.Reader)
		must(err)
		peer, err := ecdh.X25519().GenerateKey(rand.Reader)
		must(err)
		psk := make([]byte, 32)
		_, err = rand.Read(psk)
		must(err)
		b64 := base64.StdEncoding.EncodeToString
		peerConfig := map[string]any{
			"log": map[string]any{"level": "error"},
			"endpoints": []any{map[string]any{
				"type": "wireguard", "tag": "peer", "system": false, "listen_port": 21111,
				"address": []string{"10.99.0.1/32"}, "private_key": b64(peer.Bytes()), "mtu": 1408, "workers": 1,
				"peers": []any{map[string]any{"public_key": b64(client.PublicKey().Bytes()), "pre_shared_key": b64(psk), "allowed_ips": []string{"10.99.0.2/32"}}},
			}},
			"outbounds": []any{map[string]any{"type": "direct", "tag": "landing", "inet4_bind_address": "198.51.100.2"}},
			"route":     map[string]any{"rules": []any{map[string]any{"inbound": []string{"peer"}, "action": "route", "outbound": "landing"}}},
		}
		writeJSON("/tmp/fixture-forward-wg-peer.json", peerConfig)
		stop := process(ctx, "ip", "netns", "exec", "landing", "/opt/ctlvps/bin/sing-box", "run", "-c", "/tmp/fixture-forward-wg-peer.json")
		defer stop()
		dns := networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.10", Port: 15353}
		transport := networkconfig.WireGuard{Server: "198.51.100.2", ServerPort: 21111, PublicKey: b64(peer.PublicKey().Bytes()), Addresses: []string{"10.99.0.2/32"}, AllowedIPs: []string{"0.0.0.0/0"}, MTU: 1408, PersistentKeepalive: 5, Family: "ipv4", DNS: dns, ConnectTimeoutSeconds: 2, Outer: networkconfig.Direct{InterfaceID: interfaces["wan1"], SourceIPv4: &networkconfig.Address{InterfaceID: interfaces["wan1"], Address: "198.51.100.1"}, Family: "ipv4", DNS: dns}}
		path := fmt.Sprintf("/api/v1/servers/%d/egress-profiles", server.ID)
		body := map[string]any{"expected_revision": 0, "name": "forward WireGuard", "kind": "wireguard", "enabled": true, "config": transport, "credentials": networkconfig.SOCKS5Credentials{WireGuardPrivateKey: b64(client.Bytes()), WireGuardPresharedKey: b64(psk)}, "action": "create"}
		var review store.EgressPreview
		admin.request("POST", path+"/preview", body, 200, &review)
		if !review.Ready {
			panic("forward WireGuard profile preview rejected")
		}
		delete(body, "action")
		body["operation_id"], body["expected_impact"] = fmt.Sprintf("%032x", 702), review.Impact.Token
		var op store.NetworkOperation
		admin.request("POST", path, body, 202, &op)
		profile.ID, profile.Kind = op.ResourceID, "wireguard"
	} else {
		must(st.CreateEgressProfile(ctx, &profile, raw))
	}
	cfg := networkconfig.Forward{ListenMode: "address", ListenAddress: "192.0.2.1", ListenInterfaceID: interfaces["wan0"], ListenPort: 25001, Network: "tcp", TargetHost: "203.0.113.10", TargetPort: 18080, SourceMode: "cidr", SourceCIDRs: []string{"192.0.2.2/32"}, MaxTCPConnections: 8, EgressProfileID: profile.ID, EgressRevision: 1}
	if throughUDP {
		cfg.Network, cfg.MaxTCPConnections, cfg.MaxUDPSessions, cfg.UDPIdleSeconds = "udp", 0, 8, 2
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-dns" {
		cfg.TargetHost = "forward-refresh.fixture"
	}
	private := os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-private"
	enabled := !private
	if private {
		cfg.TargetHost = "private-business.fixture.test"
	}
	base := fmt.Sprintf("/api/v1/servers/%d/forwards", server.ID)
	submit := func(path, action, key string, revision int64) store.NetworkOperation {
		body := map[string]any{"action": action, "expected_revision": revision}
		if action != "delete" {
			body["name"], body["enabled"], body["config"] = "forward fixture", enabled, cfg
		}
		var view store.NetworkReadiness
		admin.request("POST", path+"/preview", body, http.StatusOK, &view)
		if !view.Ready || view.Impact == nil {
			panic(fmt.Sprintf("forward preview not ready: %+v", view.Checks))
		}
		delete(body, "action")
		body["operation_id"], body["expected_impact"] = strings.Repeat(key, 32), view.Impact.Token
		method := "POST"
		if action == "update" {
			method = "PUT"
		}
		if action == "delete" {
			method = "DELETE"
		}
		var op store.NetworkOperation
		admin.request(method, path, body, http.StatusAccepted, &op)
		admin.request(method, path, body, http.StatusAccepted, &op) // exact HTTP retry
		must(d.ReconcileNetworkOperations(ctx))
		eventually("forward apply and accounting receipt", func() bool { got, e := st.NetworkOperation(ctx, op.ID); return e == nil && got.Status == "applied" })
		return op
	}
	op := submit(base, "create", "a", 0)
	var list []domain.PortForward
	admin.request("GET", base, nil, http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != op.ResourceID {
		panic("forward list omitted accepted resource")
	}
	path := fmt.Sprintf("/api/v1/forwards/%d", op.ResourceID)
	revision := int64(1)
	setGrants := func(grants []networkconfig.ForwardGrant) {
		writeJSON("/etc/ctlvps/.fixture-security.json", secureupdate.Policy{Schema: 1, Actions: []string{"agent.configure"}, ForwardGrants: grants})
		must(os.Rename("/etc/ctlvps/.fixture-security.json", "/etc/ctlvps/security.json"))
	}
	grants := []networkconfig.ForwardGrant{{ForwardID: op.ResourceID, EgressProfileID: profile.ID, Address: "10.44.0.10", Port: 18080}}
	if private {
		// Prove the private target exists before exercising rejection paths.
		run("ip", "netns", "exec", "landing", "/fixtures/probe", "listener-probe", "10.44.0.10:18080")
		setGrants(grants)
		eventually("forward local grant diagnostics", func() bool {
			ag, e := st.GetAgentByServer(ctx, server.ID)
			var diag agentproto.Diagnostics
			_ = json.Unmarshal(ag.Diagnostics, &diag)
			return e == nil && diag.ForwardPrivateVersion == 1 && len(diag.ForwardGrants) == 1
		})
		enabled = true
		submit(path, "update", "d", revision)
		revision++
	}
	probe := func() bool {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		network := "tcp"
		if throughUDP {
			network = "udp"
		}
		cmd := exec.CommandContext(c, "ip", "netns", "exec", "landing", "/fixtures/probe", "forward-client", network, fmt.Sprintf("192.0.2.1:%d", cfg.ListenPort), "192.0.2.2")
		if throughUDP {
			cmd.Stdin = strings.NewReader("probe\n")
		}
		b, e := cmd.Output()
		var result struct {
			Value string `json:"value"`
			Error string `json:"error"`
		}
		if throughUDP {
			_, payload, _ := strings.Cut(string(b), "\n")
			b = []byte(payload)
		}
		want := "198.51.100.1"
		if throughSOCKS || throughSSH || throughWG {
			want = "198.51.100.2"
		}
		return e == nil && json.Unmarshal(b, &result) == nil && result.Error == "" && result.Value == want
	}
	eventually("forward-only service uses selected WAN", probe)
	eventually("independent forward usage", func() bool {
		rx, tx, e := st.SumTraffic(ctx, store.SubjectForward, op.ResourceID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		return e == nil && rx > 0 && tx > 0
	})
	if throughSOCKS || throughSSH || throughWG {
		fmt.Printf("PASS admin preview/create/retry -> desired -> real agent -> marked %s forward -> independent traffic\n", profile.Kind)
	} else {
		fmt.Println("PASS admin preview/create/retry -> desired -> real agent -> forward-only shared core -> selected WAN -> independent traffic")
	}
	if private {
		setGrants(nil)
		eventually("private forward grant revoked", func() bool {
			ag, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && strings.Contains(ag.ApplyError, fmt.Sprintf("forward %d network:", op.ResourceID)) && !probe()
		})
		setGrants(grants)
		eventually("private forward grant restored", probe)
		fmt.Println("PASS independent root grant enables private DNS target, revocation blocks and restoration reapplies")
	}
	if os.Getenv("NETWORK_SYSTEMD_CASE") == "forward-dns" {
		must(os.WriteFile("/tmp/ctlvps-fixture-forward-dns", []byte("private"), 0600))
		eventually("DNS rebinding cannot reach private target", func() bool {
			a, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && strings.Contains(a.ApplyError, fmt.Sprintf("forward %d network:", op.ResourceID)) && !probe()
		})
		must(os.Remove("/tmp/ctlvps-fixture-forward-dns"))
		eventually("same desired recovers after DNS returns public", probe)
		fmt.Println("PASS forward DNS pin, blocked private rebinding and local refresh recovery")
	}
	cfg.ListenPort++
	submit(path, "update", "b", revision)
	revision++
	eventually("changed forward listener", probe)
	ports, err := st.UsedListenPorts(ctx, server.ID)
	must(err)
	if ports[25001] || !ports[25002] {
		panic("old port not released after exact receipt")
	}
	fmt.Println("PASS listener edit and old-port release after cleanup receipt")
	submit(path, "delete", "c", revision)
	admin.request("GET", base, nil, http.StatusOK, &list)
	if len(list) != 0 {
		panic("retired forward remained after receipt")
	}
	ports, err = st.UsedListenPorts(ctx, server.ID)
	must(err)
	if len(ports) != 0 || probe() {
		panic("retired listener/port survived cleanup")
	}
	rx, tx, err := st.SumTraffic(ctx, store.SubjectForward, op.ResourceID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	must(err)
	if rx <= 0 || tx <= 0 {
		panic("cleanup lost forwarding usage")
	}
	fmt.Println("PASS retirement closes listener, settles final usage and releases reservations")
}
