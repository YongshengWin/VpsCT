package desired

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
	"ctlvps/internal/subscription"
)

func TestDesiredPinsNetworkRevisionAndKeepsClientConfigurationSeparate(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := domain.Server{Name: "fixture", PublicHost: "server.example.test", Enabled: true, CoreMode: domain.CoreModeStable}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	n, err := provision.NewNode(server, "", provision.Options{Protocol: domain.ProtocolShadowsocks, Port: 21001})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	b := New(s)
	legacy, err := b.Build(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(legacy)
	if strings.Contains(string(wire), `"network":`) || legacy.Hash != agentproto.ContentHash(legacy) {
		t.Fatal("legacy desired changed its wire/hash contract")
	}
	direct := networkconfig.Direct{Family: "ipv4", InterfaceID: strings.Repeat("1", 32), DNS: networkconfig.Resolver{Transport: "udp", Address: "192.0.2.53", Port: 53}}
	config, _ := json.Marshal(direct)
	profile := domain.EgressProfile{ServerID: server.ID, Name: "fixture", Kind: "direct", Enabled: true}
	if err = s.CreateEgressProfile(ctx, &profile, config); err != nil {
		t.Fatal(err)
	}
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: profile.ID, EgressRevision: 1}
	before := subscription.ProxyFor(n, nil)
	n, err = s.SetNodeNetwork(ctx, n.ID, 0, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	after := subscription.ProxyFor(n, nil)
	clientBefore, _ := json.Marshal(before)
	clientAfter, _ := json.Marshal(after)
	if string(clientBefore) != string(clientAfter) {
		t.Fatal("server binding leaked into client configuration")
	}
	bound, err := b.Build(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Nodes[0].Network == nil || bound.Nodes[0].Network.Direct.DNS.Address != "192.0.2.53" || bound.Hash == legacy.Hash || bound.Hash != agentproto.ContentHash(bound) {
		t.Fatal("desired lost its immutable direct config")
	}
	direct.DNS.Address = "192.0.2.54"
	config, _ = json.Marshal(direct)
	if _, err = s.AppendEgressRevision(ctx, profile.ID, 1, "new revision", true, config); err != nil {
		t.Fatal(err)
	}
	unchanged, err := b.Build(ctx, server)
	if err != nil || unchanged.Hash != bound.Hash {
		t.Fatal("editing a profile moved its bound revision", err)
	}
	if _, err = s.AppendEgressRevision(ctx, profile.ID, 2, "disabled", false, config); err != nil {
		t.Fatal(err)
	}
	disabled, err := b.Build(ctx, server)
	if err != nil || !disabled.Nodes[0].Blocked || disabled.Nodes[0].Network.Direct.DNS.Address != "192.0.2.53" {
		t.Fatal("disabled profile fell back or stayed usable", err)
	}
	oldPolicy := *n.Network
	if err = provision.RegenerateCredentials(&n, server); err != nil {
		t.Fatal(err)
	}
	if n.Network == nil || *n.Network != oldPolicy || n.NetworkRevision != 1 {
		t.Fatal("credential rotation changed network policy")
	}
	if err = s.UpdateNodeCredentials(ctx, &n); err != nil {
		t.Fatal(err)
	}
	rotated, err := b.Build(ctx, server)
	if err != nil || rotated.Nodes[0].Network.Policy != oldPolicy || !rotated.Nodes[0].Blocked {
		t.Fatal("rotated desired lost binding or profile block", err)
	}
	// Quota/deactivation must still produce an empty state independent of any
	// network capability or offline interface.
	server.Enabled = false
	empty, err := b.Build(ctx, server)
	if err != nil || len(empty.Nodes) != 0 {
		t.Fatal("server shutdown depends on network bindings", err)
	}
}

func TestDesiredSOCKS5PinsCredentialsAndRetainsCleanupContract(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "socks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := domain.Server{Name: "fixture", PublicHost: "server.example.test", Enabled: true, CoreMode: domain.CoreModeStable}
	if err = s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	n, err := provision.NewNode(server, "", provision.Options{Protocol: domain.ProtocolShadowsocks, Port: 21001})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	resolver := networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}
	cfg := networkconfig.SOCKS5{Server: "upstream.example.test", ServerPort: 1080, Authentication: "password", Family: "dual", DNS: resolver, Outer: networkconfig.Direct{Family: "ipv4", DNS: resolver}, ConnectTimeoutSeconds: 10}
	raw, _ := json.Marshal(cfg)
	secret := networkconfig.SOCKS5Credentials{Username: "fixture-upstream", Password: "fixture-only-password"}
	op, err := s.RequestEgressProfile(ctx, store.EgressProfileRequest{ID: strings.Repeat("a", 32), Action: "create", ServerID: server.ID, Name: "fixture", Kind: "socks5", Enabled: true, Config: raw, Credentials: &secret}, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.GetEgressProfile(ctx, op.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := s.NetworkEgressVersion(ctx, server.ID); err != nil || version != 0 {
		t.Fatal("saving a template opted server into transit", err)
	}
	before, _ := json.Marshal(subscription.ProxyFor(n, nil))
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: p.ID, EgressRevision: 1}
	n, err = s.SetNodeNetwork(ctx, n.ID, 0, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(subscription.ProxyFor(n, nil))
	if string(before) != string(after) {
		t.Fatal("server transport changed client subscription")
	}
	b := New(s)
	rec, _, err := b.Publish(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := Load(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err = agentproto.ValidateDesired(ds, server.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if ds.NetworkEgressVersion != 1 || ds.Nodes[0].Network.Direct != nil || ds.Nodes[0].Network.SOCKS5.Credentials != secret {
		t.Fatal("desired lost typed transit or pinned credentials")
	}
	newSecret := secret
	newSecret.Password += "-rotated"
	if _, err = s.RequestEgressProfile(ctx, store.EgressProfileRequest{ID: strings.Repeat("b", 32), Action: "update", ProfileID: p.ID, ExpectedRevision: 1, Name: p.Name, Kind: "socks5", Enabled: true, Config: raw, Credentials: &newSecret}, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := b.Build(ctx, server)
	if err != nil || unchanged.Hash != ds.Hash || unchanged.Nodes[0].Network.SOCKS5.Credentials != secret {
		t.Fatal("editing template credentials moved an existing binding", err)
	}
	policy.EgressRevision = 2
	if _, err = s.SetNodeNetwork(ctx, n.ID, 1, policy, nil); err != nil {
		t.Fatal(err)
	}
	rotated, err := b.Build(ctx, server)
	if err != nil || rotated.Hash == ds.Hash || rotated.Nodes[0].Network.SOCKS5.Credentials != newSecret {
		t.Fatal("explicit rebind did not rotate credentials", err)
	}
	if _, err = s.SetNodeNetwork(ctx, n.ID, 2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteEgressProfile(ctx, p.ID, 2); err != nil {
		t.Fatal(err)
	}
	server.Enabled = false
	cleanup, err := b.Build(ctx, server)
	if err != nil || len(cleanup.Nodes) != 0 || cleanup.NetworkEgressVersion != 1 || cleanup.NetworkBindingVersion != 1 {
		t.Fatal("empty cleanup dropped transit contract", err)
	}
}
