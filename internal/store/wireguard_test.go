package store

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestWireGuardIdentityAndCompatibility(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, diag, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	sid := *n.ServerID
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dns := networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}
	cfg := networkconfig.WireGuard{Server: "192.0.2.2", ServerPort: 51820, PublicKey: base64.StdEncoding.EncodeToString(peer.PublicKey().Bytes()), Addresses: []string{"10.99.0.2/32"}, AllowedIPs: []string{"0.0.0.0/0"}, Family: "ipv4", MTU: 1408, DNS: dns, Outer: networkconfig.Direct{Family: "ipv4", DNS: dns}, ConnectTimeoutSeconds: 5}
	raw, _ := json.Marshal(cfg)
	secret := networkconfig.SOCKS5Credentials{WireGuardPrivateKey: base64.StdEncoding.EncodeToString(key.Bytes())}
	req := EgressProfileRequest{ID: strings.Repeat("7", 32), Action: "create", ServerID: sid, Name: "WG fixture", Kind: "wireguard", Enabled: true, Config: raw, Credentials: &secret}
	op, err := s.RequestEgressProfile(ctx, req, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	policy := *in.Network
	policy.EgressProfileID = op.ResourceID
	policy.EgressRevision = 1
	diag.NetworkEgressVersion = agentproto.NetworkEgressVersion
	writeNetworkDiagnostics(t, s, sid, diag)
	view, err := s.PreviewNodeNetwork(ctx, n.ID, &policy)
	if err != nil || view.Ready {
		t.Fatal("old agent admitted WG", err)
	}
	diag.NetworkWireGuardVersion = 1
	writeNetworkDiagnostics(t, s, sid, diag)
	view, err = s.PreviewNodeNetwork(ctx, n.ID, &policy)
	if err != nil || !view.Ready {
		t.Fatal("WG admission failed", err, view.Checks)
	}
	if _, err = s.SetNodeNetwork(ctx, n.ID, 0, &policy, nil); err != nil {
		t.Fatal(err)
	}
	public, err := s.EgressProfileView(ctx, op.ResourceID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(public)
	if strings.Contains(string(b), secret.WireGuardPrivateKey) {
		t.Fatal("WG private key leaked")
	}
	var encrypted string
	if err = s.db.QueryRow(`SELECT credentials FROM egress_profile_credentials WHERE profile_id=?`, op.ResourceID).Scan(&encrypted); err != nil || !strings.HasPrefix(encrypted, secretPrefix) || strings.Contains(encrypted, secret.WireGuardPrivateKey) {
		t.Fatal("unencrypted credential", err)
	}
	n2 := n
	n2.ID = 0
	n2.ListenPort++
	n2.Port++
	n2.Network = nil
	n2.NetworkRevision = 0
	if err = s.CreateNode(ctx, &n2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetNodeNetwork(ctx, n2.ID, 0, &policy, nil); err == nil {
		t.Fatal("private identity shared across consumers")
	}
	if _, err = s.SetNodeNetwork(ctx, n.ID, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetNodeNetwork(ctx, n2.ID, 0, &policy, nil); err == nil {
		t.Fatal("detach released identity while old agent can still run it")
	}
	if v, err := s.NetworkWireGuardVersion(ctx, sid); err != nil || v != 1 {
		t.Fatal("cleanup compatibility lost", err)
	}
	if err = s.DeleteServer(ctx, sid); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM wireguard_key_owners`).Scan(&count); err != nil || count != 1 {
		t.Fatal("server deletion released cryptographic identity", err)
	}
}
