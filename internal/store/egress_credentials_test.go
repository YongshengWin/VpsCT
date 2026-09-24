package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestSOCKS5CredentialsAreEncryptedVersionedAndExcludedFromPublicViews(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, _, _ := readyNetworkFixture(t, s)
	n, _ := s.GetNode(ctx, in.NodeID)
	cfg := networkconfig.SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "password", UDP: true, Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53},
		Outer: networkconfig.Direct{Family: "ipv4", DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.54", Port: 53}}, ConnectTimeoutSeconds: 10}
	raw, _ := json.Marshal(cfg)
	secret := networkconfig.SOCKS5Credentials{Username: "private-upstream-user", Password: "private-upstream-password"}
	write := EgressProfileRequest{ID: strings.Repeat("8", 32), Action: "create", ServerID: *n.ServerID, Name: "SOCKS fixture", Kind: "socks5", Enabled: true, Config: raw, Credentials: &secret}
	preview, err := s.PreviewEgressProfile(ctx, write)
	if err != nil || !preview.Ready {
		t.Fatal(preview, err)
	}
	write.ExpectedImpact = preview.Impact.Token
	op, err := s.RequestReviewedEgressProfile(ctx, write, domain.AuditEvent{})
	if err != nil || op.Status != "saved" {
		t.Fatal(op, err)
	}
	profile, _ := s.GetEgressProfile(ctx, op.ResourceID)
	policy := *in.Network
	policy.EgressProfileID, policy.EgressRevision = profile.ID, 1
	if _, err = s.SetNodeNetwork(ctx, n.ID, 0, &policy, nil); err != nil {
		t.Fatal(err)
	}
	revision, err := s.GetEgressRevision(ctx, profile.ServerID, profile.ID, 1)
	if err != nil || !revision.HasCredentials {
		t.Fatal(revision, err)
	}
	b, _ := json.Marshal([]any{preview, op, profile, revision})
	if strings.Contains(string(b), secret.Username) || strings.Contains(string(b), secret.Password) {
		t.Fatal("public view contains upstream credentials")
	}
	var encrypted string
	if err := s.db.QueryRow(`SELECT credentials FROM egress_profile_credentials WHERE profile_id=? AND revision=1`, profile.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, secretPrefix) || strings.Contains(encrypted, secret.Password) || strings.Contains(encrypted, secret.Username) {
		t.Fatal("credentials were not encrypted")
	}
	if _, err := s.decrypt(egressCredentialLabel(profile.ServerID, profile.ID, 2), encrypted); err == nil {
		t.Fatal("ciphertext accepted under a different revision")
	}
	if _, err := s.egressCredentials(ctx, s.db, profile.ServerID+1, profile.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign server can retrieve credentials", err)
	}
	if _, err := s.db.Exec(`UPDATE egress_profile_credentials SET credentials=credentials WHERE profile_id=?`, profile.ID); err == nil {
		t.Fatal("immutable credential revision was editable")
	}
	// Omission preserves the existing credential pair in the new revision.
	write = EgressProfileRequest{ID: strings.Repeat("9", 32), Action: "update", ProfileID: profile.ID, ExpectedRevision: 1, Name: profile.Name, Kind: "socks5", Enabled: true, Config: raw}
	op, err = s.RequestEgressProfile(ctx, write, domain.AuditEvent{})
	if err != nil || op.ResourceRevision != 2 {
		t.Fatal(op, err)
	}
	got, err := s.egressCredentials(ctx, s.db, profile.ServerID, profile.ID, 2)
	if err != nil || got != secret {
		t.Fatal("omitted credentials did not preserve their original pair", err)
	}
	// No-auth is an explicit new version; pinned old credentials remain usable.
	cfg.Authentication = "none"
	write.Config, _ = json.Marshal(cfg)
	write.ID, write.ExpectedRevision = strings.Repeat("a", 32), 2
	if _, err = s.RequestEgressProfile(ctx, write, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	revision, err = s.GetEgressRevision(ctx, profile.ServerID, profile.ID, 3)
	if err != nil || revision.HasCredentials {
		t.Fatal("no-auth new revision retained credentials", err)
	}
	got, err = s.egressCredentials(ctx, s.db, profile.ServerID, profile.ID, 1)
	if err != nil || got != secret {
		t.Fatal("auth change overwrote a pinned revision", err)
	}
	cfg.Authentication = "password"
	write.Config, _ = json.Marshal(cfg)
	write.ID, write.ExpectedRevision = strings.Repeat("b", 32), 3
	if _, err = s.RequestEgressProfile(ctx, write, domain.AuditEvent{}); err == nil {
		t.Fatal("returning from no-auth guessed credentials from an older version")
	}
	profile, _ = s.GetEgressProfile(ctx, profile.ID)
	if profile.CurrentRevision != 3 {
		t.Fatal("credential validation failure partially saved a version")
	}
	deployed, err := s.DeployedNodeNetworks(ctx, profile.ServerID)
	if err != nil || len(deployed) != 1 || deployed[0].Credentials == nil || *deployed[0].Credentials != secret || deployed[0].Revision.Revision != 1 {
		t.Fatal("desired snapshot failed to keep the pinned credential revision", err)
	}
	b, _ = json.Marshal(deployed)
	if strings.Contains(string(b), secret.Password) || strings.Contains(string(b), secret.Username) {
		t.Fatal("desired store snapshot accidentally exports its credential member")
	}
	if _, err = s.SetNodeNetwork(ctx, n.ID, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteEgressProfile(ctx, profile.ID, 3); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM egress_profile_credentials WHERE profile_id=?`, profile.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted profile retained orphaned credentials", err)
	}
}
