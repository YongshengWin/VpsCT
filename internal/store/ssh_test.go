package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"golang.org/x/crypto/ssh"
)

func TestSSHCredentialsAndStickyCompatibility(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, diag, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	sid := *n.ServerID
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	dns := networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}
	cfg := networkconfig.SSH{Server: "192.0.2.2", ServerPort: 22, Authentication: "password", HostKeys: []string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}, Family: "ipv4", DNS: dns, Outer: networkconfig.Direct{Family: "ipv4", DNS: dns}, ConnectTimeoutSeconds: 5}
	raw, _ := json.Marshal(cfg)
	secret := networkconfig.SOCKS5Credentials{Username: "fixture-user", Password: "fixture-secret"}
	write := EgressProfileRequest{ID: strings.Repeat("8", 32), Action: "create", ServerID: sid, Name: "SSH fixture", Kind: "ssh", Enabled: true, Config: raw, Credentials: &secret}
	preview, err := s.PreviewEgressProfile(ctx, write)
	if err != nil || !preview.Ready {
		t.Fatal("profile preview failed", err)
	}
	write.ExpectedImpact = preview.Impact.Token
	op, err := s.RequestReviewedEgressProfile(ctx, write, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	policy := *in.Network
	policy.EgressProfileID, policy.EgressRevision = op.ResourceID, 1
	diag.NetworkEgressVersion = agentproto.NetworkEgressVersion
	writeNetworkDiagnostics(t, s, sid, diag)
	view, err := s.PreviewNodeNetwork(ctx, n.ID, &policy)
	if err != nil || view.Ready {
		t.Fatal("SOCKS-only agent admitted SSH", err)
	}
	diag.NetworkSSHVersion = agentproto.NetworkSSHVersion
	writeNetworkDiagnostics(t, s, sid, diag)
	view, err = s.PreviewNodeNetwork(ctx, n.ID, &policy)
	if err != nil || !view.Ready {
		t.Fatal("SSH agent rejected", err)
	}
	if _, err = s.SetNodeNetwork(ctx, n.ID, 0, &policy, nil); err != nil {
		t.Fatal(err)
	}
	public, err := s.EgressProfileView(ctx, op.ResourceID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(public)
	if strings.Contains(string(b), secret.Username) || strings.Contains(string(b), secret.Password) {
		t.Fatal("credential escaped public view")
	}
	var encrypted string
	if err = s.db.QueryRow(`SELECT credentials FROM egress_profile_credentials WHERE profile_id=?`, op.ResourceID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, secretPrefix) || strings.Contains(encrypted, secret.Password) {
		t.Fatal("SSH credential not encrypted")
	}
	write = EgressProfileRequest{ID: strings.Repeat("9", 32), Action: "update", ProfileID: op.ResourceID, ExpectedRevision: 1, Name: "SSH fixture", Kind: "ssh", Enabled: true, Config: raw}
	if _, err = s.RequestEgressProfile(ctx, write, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.egressCredentials(ctx, s.db, sid, op.ResourceID, 2)
	if err != nil || got != secret {
		t.Fatal("retained credentials changed", err)
	}
	deployed, err := s.DeployedNodeNetworks(ctx, sid)
	if err != nil || len(deployed) != 1 || deployed[0].Credentials == nil || *deployed[0].Credentials != secret || deployed[0].Revision.Revision != 1 {
		t.Fatal("pinned SSH revision changed", err)
	}
	if _, err = s.SetNodeNetwork(ctx, n.ID, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteEgressProfile(ctx, op.ResourceID, 2); err != nil {
		t.Fatal(err)
	}
	if version, err := s.NetworkSSHVersion(ctx, sid); err != nil || version != 1 {
		t.Fatal("SSH cleanup contract lost", err)
	}
	diag.NetworkSSHVersion = 0
	writeNetworkDiagnostics(t, s, sid, diag)
	view, err = s.PreviewNodeNetwork(ctx, n.ID, nil)
	if err != nil || view.Ready {
		t.Fatal("old agent admitted after SSH deletion", err)
	}
}
