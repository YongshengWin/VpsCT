//go:build linux

package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"ctlvps/internal/desired"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/share"
	"ctlvps/internal/store"
	"encoding/base64"
	"fmt"
	"os"
)

func testWireGuardSystemd(ctx context.Context, st *store.Store, d *desired.Builder, shares *share.Manager, server domain.Server, interfaces map[string]string, admin *fixtureAdmin) {
	if os.Getenv("NETWORK_TEST_CLIENT") != "shadowsocks-rust" {
		panic("WireGuard fixture needs independent SS client")
	}
	client, err := ecdh.X25519().GenerateKey(rand.Reader)
	must(err)
	peer, err := ecdh.X25519().GenerateKey(rand.Reader)
	must(err)
	psk := make([]byte, 32)
	_, err = rand.Read(psk)
	must(err)
	b64 := base64.StdEncoding.EncodeToString
	writeJSON("/tmp/fixture-wg-peer.json", map[string]any{"log": map[string]any{"level": "error"}, "endpoints": []any{map[string]any{"type": "wireguard", "tag": "peer", "system": false, "listen_port": 21111, "address": []string{"10.99.0.1/32"}, "private_key": b64(peer.Bytes()), "mtu": 1408, "workers": 1, "peers": []any{map[string]any{"public_key": b64(client.PublicKey().Bytes()), "pre_shared_key": b64(psk), "allowed_ips": []string{"10.99.0.2/32"}}}}}, "outbounds": []any{map[string]any{"type": "direct", "tag": "landing", "inet4_bind_address": "198.51.100.2"}}, "route": map[string]any{"rules": []any{map[string]any{"inbound": []string{"peer"}, "action": "route", "outbound": "landing"}}}})
	stopPeer := process(ctx, "ip", "netns", "exec", "landing", "/opt/ctlvps/bin/sing-box", "run", "-c", "/tmp/fixture-wg-peer.json")
	defer stopPeer()
	sh := domain.Share{Name: "WireGuard transit fixture", Targets: []domain.ShareTarget{{ServerID: server.ID, Protocols: []string{"ss"}}}}
	_, err = shares.Create(ctx, &sh)
	must(err)
	nodes, err := st.ListNodes(ctx, store.NodeFilter{ShareID: &sh.ID})
	must(err)
	if len(nodes) != 1 {
		panic("missing WireGuard consumer")
	}
	n := nodes[0]
	dns := networkconfig.Resolver{Transport: "tcp", Address: "203.0.113.10", Port: 15353}
	cfg := networkconfig.WireGuard{Server: "198.51.100.2", ServerPort: 21111, PublicKey: b64(peer.PublicKey().Bytes()), Addresses: []string{"10.99.0.2/32"}, AllowedIPs: []string{"0.0.0.0/0"}, MTU: 1408, PersistentKeepalive: 5, Family: "ipv4", DNS: dns, ConnectTimeoutSeconds: 2, Outer: networkconfig.Direct{InterfaceID: interfaces["wan1"], SourceIPv4: &networkconfig.Address{InterfaceID: interfaces["wan1"], Address: "198.51.100.1"}, Family: "ipv4", DNS: dns}}
	profilePath := fmt.Sprintf("/api/v1/servers/%d/egress-profiles", server.ID)
	body := map[string]any{"expected_revision": 0, "name": "WireGuard fixture", "kind": "wireguard", "enabled": true, "config": cfg, "credentials": networkconfig.SOCKS5Credentials{WireGuardPrivateKey: b64(client.Bytes()), WireGuardPresharedKey: b64(psk)}, "action": "create"}
	var review store.EgressPreview
	admin.request("POST", profilePath+"/preview", body, 200, &review)
	if !review.Ready {
		panic("WireGuard profile review rejected")
	}
	delete(body, "action")
	body["operation_id"], body["expected_impact"] = fmt.Sprintf("%032x", 601), review.Impact.Token
	var op store.NetworkOperation
	admin.request("POST", profilePath, body, 202, &op)
	profileID := op.ResourceID
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "override", OnUnavailable: "block", EgressProfileID: profileID, EgressRevision: 1}
	bindingPath := fmt.Sprintf("/api/v1/nodes/%d/network", n.ID)
	binding := map[string]any{"network": policy, "advertise_host": "198.51.100.1"}
	var ready store.NetworkReadiness
	admin.request("POST", bindingPath+"/preview", binding, 200, &ready)
	if !ready.Ready {
		panic(fmt.Sprintf("WireGuard node not ready: %+v", ready.Checks))
	}
	binding["operation_id"], binding["expected_revision"], binding["expected_impact"] = fmt.Sprintf("%032x", 602), n.NetworkRevision, ready.Impact.Token
	admin.request("PUT", bindingPath, binding, 202, &op)
	waitApplied := func() {
		must(d.ReconcileNetworkOperations(ctx))
		rec, e := st.LatestDesiredState(ctx, server.ID)
		must(e)
		eventually("WireGuard actual application", func() bool {
			ag, e := st.GetAgentByServer(ctx, server.ID)
			return e == nil && ag.AppliedRevision == rec.Revision && ag.ApplyError == ""
		})
	}
	waitApplied()
	n, err = st.GetNode(ctx, n.ID)
	must(err)
	stopClient := startClient(ctx, 0, n)
	defer stopClient()
	eventually("WireGuard ingress client", func() bool { _, e := probe(0, "ready", ""); return e == nil })
	// WireGuard begins its first handshake while the apply lease is still
	// fenced. Its protocol retry occurs after the guard opens, asynchronously.
	eventually("WireGuard authenticated handshake", func() bool { got, e := probe(0, "tcp", "203.0.113.10"); return e == nil && got == "198.51.100.2" })
	for _, target := range []string{"203.0.113.10", "binding.test"} {
		got, e := probe(0, "tcp", target)
		if e != nil || got != "198.51.100.2" {
			dumpNetworkFailure()
			panic(fmt.Sprintf("WireGuard TCP/DNS failed: %s %v", got, e))
		}
	}
	if got, e := probe(0, "udp", "203.0.113.10"); e != nil || got != "198.51.100.2" {
		dumpNetworkFailure()
		panic(fmt.Sprintf("WireGuard UDP failed: %s %v", got, e))
	}
	eventually("WireGuard traffic assigned to share", func() bool {
		v, e := st.GetShare(ctx, sh.ID)
		return e == nil && v.UsedUpload > 0 && v.UsedDownload > 0
	})
	fmt.Println("PASS WireGuard admin create/bind -> real agent/helper/systemd -> selected WAN -> independent peer identity -> TCP/UDP and business DNS -> share accounting")
	// Disable the actual immutable profile; the normal shared-process apply
	// must fence any existing WireGuard transport, not silently route direct.
	path := fmt.Sprintf("/api/v1/egress-profiles/%d", profileID)
	body = map[string]any{"expected_revision": 1, "name": "WireGuard fixture", "kind": "wireguard", "enabled": false, "config": cfg, "action": "update"}
	admin.request("POST", path+"/preview", body, 200, &review)
	if !review.Ready {
		panic("WireGuard disable review rejected")
	}
	delete(body, "action")
	body["operation_id"], body["expected_impact"] = fmt.Sprintf("%032x", 603), review.Impact.Token
	admin.request("PUT", path, body, 202, &op)
	waitApplied()
	if _, e := probe(0, "tcp", "203.0.113.10"); e == nil {
		panic("disabled WireGuard fell back to direct")
	}
	fmt.Println("PASS WireGuard profile disable blocks business with no direct fallback")
}
