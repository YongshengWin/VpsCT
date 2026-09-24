//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"golang.org/x/crypto/ssh"
)

// Only launched in the disposable landing namespace. No shell, exec, SFTP,
// arbitrary targets or operator credentials are available to this SSH server.
func sshFixturePeer() {
	raw, err := os.ReadFile("/tmp/fixture-ssh-host.pem")
	must(err)
	signer, err := ssh.ParsePrivateKey(raw)
	must(err)
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == "forward" && string(p) == "fixture-only" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	config.AddHostKey(signer)
	l, err := net.Listen("tcp", "198.51.100.2:2222")
	must(err)
	defer l.Close()
	for {
		c, err := l.Accept()
		must(err)
		go func() {
			defer c.Close()
			conn, channels, requests, err := ssh.NewServerConn(c, config)
			if err != nil {
				return
			}
			defer conn.Close()
			go ssh.DiscardRequests(requests)
			if host, _, _ := net.SplitHostPort(c.RemoteAddr().String()); host != "198.51.100.1" {
				return
			}
			for incoming := range channels {
				var target struct {
					Host       string
					Port       uint32
					Origin     string
					OriginPort uint32
				}
				if incoming.ChannelType() != "direct-tcpip" || ssh.Unmarshal(incoming.ExtraData(), &target) != nil || target.Host != "203.0.113.10" || (target.Port != 18080 && target.Port != 15353) {
					incoming.Reject(ssh.Prohibited, "fixture target only")
					continue
				}
				dialer := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP("198.51.100.2")}}
				upstream, err := dialer.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
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
}

func testSSHSystemd(ctx context.Context, st *store.Store, d *desired.Builder, shares *share.Manager, server domain.Server, interfaces map[string]string, admin *fixtureAdmin) {
	if os.Getenv("NETWORK_TEST_CLIENT") != "shadowsocks-rust" {
		panic("SSH fixture needs independent SS client")
	}
	_, host, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	signer, err := ssh.NewSignerFromKey(host)
	must(err)
	der, err := x509.MarshalPKCS8PrivateKey(host)
	must(err)
	must(os.WriteFile("/tmp/fixture-ssh-host.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600))
	stopPeer := process(ctx, "ip", "netns", "exec", "landing", "/fixtures/integration", "ssh-peer")
	defer stopPeer()
	eventually("SSH peer listener", func() bool {
		c, e := net.DialTimeout("tcp", "198.51.100.2:2222", 100*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	})
	sh := domain.Share{Name: "SSH transit fixture", Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	_, err = shares.Create(ctx, &sh)
	must(err)
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	must(err)
	if len(nodes) != 1 {
		panic("missing SSH consumer")
	}
	n := nodes[0]
	dns := networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.10", Port: 15353}
	cfg := networkconfig.SSH{Server: "198.51.100.2", ServerPort: 2222, Authentication: "password", HostKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}, Family: "ipv4", DNS: dns, ConnectTimeoutSeconds: 2, Outer: networkconfig.Direct{InterfaceID: interfaces["wan1"], SourceIPv4: &networkconfig.Address{InterfaceID: interfaces["wan1"], Address: "198.51.100.1"}, Family: "ipv4", DNS: dns}}
	profilePath := fmt.Sprintf("/api/v1/servers/%d/egress-profiles", server.ID)
	body := map[string]any{"expected_revision": 0, "name": "SSH fixture", "kind": "ssh", "enabled": true, "config": cfg, "credentials": networkconfig.SOCKS5Credentials{Username: "forward", Password: "fixture-only"}, "action": "create"}
	var review store.EgressPreview
	admin.request("POST", profilePath+"/preview", body, 200, &review)
	if !review.Ready {
		panic("SSH profile review rejected")
	}
	delete(body, "action")
	body["operation_id"], body["expected_impact"] = fmt.Sprintf("%032x", 501), review.Impact.Token
	var op store.NetworkOperation
	admin.request("POST", profilePath, body, 202, &op)
	profileID := op.ResourceID
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "override", OnUnavailable: "block", EgressProfileID: profileID, EgressRevision: 1}
	bindingPath := fmt.Sprintf("/api/v1/nodes/%d/network", n.ID)
	binding := map[string]any{"network": policy, "advertise_host": "198.51.100.1"}
	var ready store.NetworkReadiness
	admin.request("POST", bindingPath+"/preview", binding, 200, &ready)
	if !ready.Ready {
		panic(fmt.Sprintf("SSH node not ready: %+v", ready.Checks))
	}
	binding["operation_id"], binding["expected_revision"], binding["expected_impact"] = fmt.Sprintf("%032x", 502), n.NetworkRevision, ready.Impact.Token
	admin.request("PUT", bindingPath, binding, 202, &op)
	waitApplied := func() {
		must(d.ReconcileNetworkOperations(ctx))
		rec, e := st.LatestDesiredState(ctx, server.ID)
		must(e)
		eventually("SSH actual application", func() bool {
			ag, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && ag.AppliedRevision == rec.Revision && ag.ApplyError == ""
		})
	}
	waitApplied()
	n, err = st.GetNode(ctx, n.ID)
	must(err)
	stopClient := startClient(ctx, 0, n)
	defer stopClient()
	eventually("SSH ingress client", func() bool { _, e := probe(0, "ready", ""); return e == nil })
	for _, target := range []string{"203.0.113.10", "binding.test"} {
		got, e := probe(0, "tcp", target)
		if e != nil || got != "198.51.100.2" {
			dumpNetworkFailure()
			panic(fmt.Sprintf("SSH TCP/DNS failed: %s %v", got, e))
		}
	}
	if _, e := probe(0, "udp", "203.0.113.10"); e == nil {
		panic("SSH admitted UDP")
	}
	eventually("SSH traffic assigned to share", func() bool {
		v, e := st.GetShare(ctx, sh.ID)
		return e == nil && v.UsedUpload > 0 && v.UsedDownload > 0
	})
	fmt.Println("PASS SSH admin create/bind -> real agent/helper/systemd -> selected WAN -> pinned host key -> TCP business DNS -> share accounting; UDP rejected")
	// Disable the actual immutable profile; the normal shared-process apply
	// must fence any existing SSH transport, not silently route direct.
	path := fmt.Sprintf("/api/v1/egress-profiles/%d", profileID)
	body = map[string]any{"expected_revision": 1, "name": "SSH fixture", "kind": "ssh", "enabled": false, "config": cfg, "action": "update"}
	admin.request("POST", path+"/preview", body, 200, &review)
	if !review.Ready {
		panic("SSH disable review rejected")
	}
	delete(body, "action")
	body["operation_id"], body["expected_impact"] = fmt.Sprintf("%032x", 503), review.Impact.Token
	admin.request("PUT", path, body, 202, &op)
	waitApplied()
	if _, e := probe(0, "tcp", "203.0.113.10"); e == nil {
		panic("disabled SSH fell back to direct")
	}
	fmt.Println("PASS SSH profile disable blocks business with no direct fallback")
}
