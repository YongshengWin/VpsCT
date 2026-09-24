package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/netinventory"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/networkguard"
)

func TestLiteralSOCKSOfflineRecoveryTracksRevocationWithoutPollingHealthyNodes(t *testing.T) {
	for _, address := range []string{"192.0.2.2", "10.23.0.2"} {
		t.Run(address, func(t *testing.T) {
			a, ds, _ := recoveryFixture(t)
			ds.Nodes[0].Network.SOCKS5.Config.Server = address
			ds.Hash = agentproto.ContentHash(ds)
			a.State.NetworkDesiredHash = ds.Hash
			raw, _ := json.Marshal(ds)
			if err := a.saveNetworkDesired(raw); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if a.networkRefreshCandidate(now) == nil {
				t.Fatal("literal endpoint cannot recover while controller is offline")
			}
			a.bindings.plan = &networkguard.Plan{Bindings: []networkguard.Binding{{NodeID: 1, Applied: networkconfig.Resolved{SOCKS5: &networkconfig.ResolvedSOCKS5{Address: address, Port: 1080}}}}}
			if a.networkRefreshDue(now) {
				t.Fatal("healthy literal node was scheduled repeatedly")
			}
			a.bindings.health = map[int64]string{1: "transport permission revoked"}
			if a.networkRefreshCandidate(now) == nil {
				t.Fatal("revoked literal endpoint never rechecks current root policy")
			}
			a.networkRetryAt = now.Add(time.Second)
			if a.networkRefreshDue(now) {
				t.Fatal("failed private grant retries exceed budget")
			}
			a.networkRetryAt = time.Time{}
			a.bindings.health = nil
			a.bindings.plan.Bindings[0].Pending = true
			if a.networkRefreshCandidate(now) == nil {
				t.Fatal("restored root grant cannot unblock an offline pending node")
			}
		})
	}
}

func TestSOCKS5DNSBatchResumesWithoutStarvingLaterNodes(t *testing.T) {
	r, _, ds := bindingAgentFixture(t)
	base, direct, unmanaged := ds.Nodes[0], ds.Nodes[1], ds.Nodes[2]
	ds.Nodes = nil
	for id := int64(1); id <= 4; id++ {
		n := base
		n.NodeID, n.ListenPort = id, 21000+int(id)
		cfg := networkconfig.SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "none", Family: "dual", ConnectTimeoutSeconds: 10,
			DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}, Outer: *base.Network.Direct}
		if id == 4 {
			cfg.Server = "192.0.2.2"
		}
		network := *base.Network
		network.Direct, network.SOCKS5 = nil, &agentproto.SOCKS5Egress{Config: cfg}
		n.Network = &network
		ds.Nodes = append(ds.Nodes, n)
	}
	direct.NodeID, direct.ListenPort = 5, 21005
	unmanaged.NodeID, unmanaged.ListenPort = 6, 21006
	ds.Nodes = append(ds.Nodes, direct, unmanaged)
	var attempted []int64
	for batch := 0; batch < 4; batch++ {
		ctx, cancel := context.WithCancel(context.Background())
		r.resolveSOCKS = func(ctx context.Context, id int64, p networkconfig.Node, cfg networkconfig.SOCKS5, snapshot *agentproto.NetworkSnapshot, v4 bool, now time.Time, _ []networkconfig.TransportGrant) (networkconfig.Resolved, error) {
			if id == 4 {
				return netinventory.ResolveSOCKS5(p, cfg, snapshot, v4, now)
			}
			if ctx.Err() != nil {
				t.Fatal("started another domain after the batch budget expired")
			}
			attempted = append(attempted, id)
			// Expire the inherited query budget deterministically; this
			// fixture's local guard operations do not depend on that context.
			cancel()
			if id < 3 {
				return networkconfig.Resolved{}, context.DeadlineExceeded
			}
			resolved, err := netinventory.ResolveBinding(p, &cfg.Outer, snapshot, v4, now)
			resolved.SOCKS5 = &networkconfig.ResolvedSOCKS5{Host: cfg.Server, Address: "192.0.2.2", Port: cfg.ServerPort}
			resolved.SOCKS5.SetDNSLifetime(now, 300)
			return resolved, err
		}
		tx, err := r.prepare(ctx, ds, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, n := range tx.nodes {
			ids = append(ids, n.NodeID)
		}
		want := []int64{4, 5, 6}
		if batch >= 2 {
			want = []int64{3, 4, 5, 6}
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("batch %d changed order or dropped independent/cached nodes: %v", batch, ids)
		}
		if err := tx.finish(context.Background(), map[string]bool{"singbox": true, "snell": true}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(attempted, []int64{1, 2, 3, 1}) {
		t.Fatal("later DNS node starved or a fresh pin was queried again", attempted)
	}
}

func recoveryFixture(t *testing.T) (*Agent, *agentproto.DesiredState, []byte) {
	t.Helper()
	id := strings.Repeat("1", 32)
	cfg := networkconfig.SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "password", Family: "dual", ConnectTimeoutSeconds: 10,
		DNS:   networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53},
		Outer: networkconfig.Direct{InterfaceID: id, Family: "dual", DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}}}
	ds := &agentproto.DesiredState{ServerID: 1, Revision: 2, NetworkBindingVersion: 1, NetworkEgressVersion: 1, Nodes: []agentproto.NodeSpec{
		{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 21001, Network: &agentproto.NodeNetworkSpec{
			Policy: networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block", EgressProfileID: 1, EgressRevision: 1},
			SOCKS5: &agentproto.SOCKS5Egress{Config: cfg, Credentials: networkconfig.SOCKS5Credentials{Username: "fixture", Password: "fixture-only"}}}},
	}}
	ds.Hash = agentproto.ContentHash(ds)
	a := &Agent{StateDir: t.TempDir(), State: &State{ServerID: 1, ServerURL: "https://controller.invalid", AgentToken: "fixture", NetworkBindingVersion: 1, NetworkEgressVersion: 1, NetworkDesiredRevision: ds.Revision, NetworkDesiredHash: ds.Hash}, bindings: &bindingRuntime{}}
	raw, err := json.Marshal(ds)
	if err != nil {
		t.Fatal(err)
	}
	return a, ds, raw
}

func TestNetworkRecoveryExactLatestIntentAcrossRestart(t *testing.T) {
	a, ds, raw := recoveryFixture(t)
	if err := a.saveNetworkDesired(raw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.StateDir, networkRecoveryFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("recovery secret permissions", err)
	}
	restarted := &Agent{StateDir: a.StateDir, State: a.State, bindings: &bindingRuntime{}}
	if err := restarted.loadNetworkDesired(); err != nil {
		t.Fatal(err)
	}
	got := restarted.networkRefreshCandidate(time.Now())
	if got == nil || got.Hash != ds.Hash || got.Nodes[0].Network.SOCKS5.Credentials.Password != "fixture-only" {
		t.Fatal("accepted offline intent unavailable")
	}
	got.Nodes[0].AllowPrivate = true
	got.Nodes[0].Network.SOCKS5.Credentials.Password = "modified"
	again := restarted.networkRefreshCandidate(time.Now())
	if again == nil || again.Nodes[0].AllowPrivate || again.Nodes[0].Network.SOCKS5.Credentials.Password != "fixture-only" {
		t.Fatal("local application mutated recovery intent")
	}
	// A newer accepted generation may have failed after revoking the old one.
	// Neither an in-memory old target nor its disk copy may revive it.
	restarted.State.NetworkDesiredRevision++
	restarted.State.NetworkDesiredHash = strings.Repeat("a", 64)
	if restarted.networkRefreshCandidate(time.Now()) != nil {
		t.Fatal("old cached credential resurrected")
	}
	if err := restarted.loadNetworkDesired(); err == nil {
		t.Fatal("old disk cache accepted")
	}
}

func TestNetworkRecoveryRejectsUnacceptedTamperedAndPublicFiles(t *testing.T) {
	a, ds, raw := recoveryFixture(t)
	if err := a.saveNetworkDesired(raw); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*agentproto.DesiredState){
		func(d *agentproto.DesiredState) { d.Revision++ },
		func(d *agentproto.DesiredState) { d.ServerID++ },
		func(d *agentproto.DesiredState) {
			d.Nodes[0].Network.SOCKS5.Config.Server = "other.example"
			d.Hash = agentproto.ContentHash(d)
		},
	} {
		var copy agentproto.DesiredState
		json.Unmarshal(raw, &copy)
		edit(&copy)
		b, _ := json.Marshal(copy)
		if _, err := decodeNetworkRecovery(b, a.State); err == nil {
			t.Fatal("unaccepted recovery target allowed")
		}
	}
	path := filepath.Join(a.StateDir, networkRecoveryFile)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.loadNetworkDesired(); err == nil {
		t.Fatal("world-readable recovery accepted")
	}
	os.Remove(path)
	other := filepath.Join(a.StateDir, "other")
	os.WriteFile(other, raw, 0600)
	os.Symlink(other, path)
	if err := a.loadNetworkDesired(); err == nil {
		t.Fatal("symlink recovery accepted")
	}
	ds.Nodes[0].Blocked = true
	ds.Hash = agentproto.ContentHash(ds)
	a.State.NetworkDesiredHash = ds.Hash
	b, _ := json.Marshal(ds)
	cache, err := decodeNetworkRecovery(b, a.State)
	if err != nil || len(cache.nodes) != 0 {
		t.Fatal("blocked node scheduled local refresh", err)
	}
}

func TestSOCKS5DNSRenewalAndChangeBeforeCoreApply(t *testing.T) {
	for _, mode := range []string{"same", "changed", "failed"} {
		t.Run(mode, func(t *testing.T) {
			r, f, ds := bindingAgentFixture(t)
			ds.Nodes = ds.Nodes[:1]
			n := ds.Nodes[0].Network
			cfg := networkconfig.SOCKS5{Server: "upstream.example", ServerPort: 1080, Authentication: "none", Family: "dual", ConnectTimeoutSeconds: 10, DNS: networkconfig.Resolver{Transport: "tcp", Address: "192.0.2.53", Port: 53}, Outer: *n.Direct}
			n.Direct = nil
			n.SOCKS5 = &agentproto.SOCKS5Egress{Config: cfg}
			calls := 0
			r.resolveSOCKS = func(_ context.Context, _ int64, p networkconfig.Node, c networkconfig.SOCKS5, s *agentproto.NetworkSnapshot, v4 bool, now time.Time, _ []networkconfig.TransportGrant) (networkconfig.Resolved, error) {
				calls++
				if calls > 1 && mode == "failed" {
					return networkconfig.Resolved{}, errors.New("fixture DNS unavailable")
				}
				resolved, err := netinventory.ResolveBinding(p, &c.Outer, s, v4, now)
				pin := &networkconfig.ResolvedSOCKS5{Host: c.Server, Address: "192.0.2.2", Port: c.ServerPort}
				if calls == 1 {
					now = now.Add(-31 * time.Second)
				} // refresh due, not expired
				if calls > 1 && mode == "changed" {
					pin.Address = "192.0.2.3"
				}
				pin.SetDNSLifetime(now, 60)
				resolved.SOCKS5 = pin
				return resolved, err
			}
			tx, err := r.prepare(context.Background(), ds, nil)
			if err != nil || len(tx.failures) != 0 {
				t.Fatal(err)
			}
			if err := tx.finish(context.Background(), map[string]bool{"singbox": true}); err != nil {
				t.Fatal(err)
			}
			waitBinding(t, func() bool { return f.granted(1) })
			f.mu.Lock()
			installs := f.installs
			token := f.plan.Token
			f.mu.Unlock()
			tx, err = r.prepare(context.Background(), ds, nil)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatal("due DNS pin not refreshed")
			}
			if mode == "same" {
				if err := tx.finish(context.Background(), map[string]bool{"singbox": true}); err != nil {
					t.Fatal(err)
				}
				f.mu.Lock()
				unchanged := f.installs == installs && f.plan.Token == token && f.grants[1]
				f.mu.Unlock()
				if !unchanged {
					t.Fatal("same IP renewal flushed leases or reinstalled firewall")
				}
				if r.plan.Bindings[0].Applied.SOCKS5.DNSRefreshDue(time.Now()) {
					t.Fatal("lifetime not renewed")
				}
			} else {
				if f.granted(1) || !r.plan.Bindings[0].Pending {
					t.Fatal("old endpoint remained active before core apply")
				}
				if mode == "failed" && (len(tx.nodes) != 0 || len(tx.failures) != 1) {
					t.Fatal("failed lookup reached core")
				}
				if err := tx.finish(context.Background(), map[string]bool{"singbox": false}); err != nil {
					t.Fatal(err)
				}
				if f.granted(1) || !r.plan.Bindings[0].Pending {
					t.Fatal("failed core/rollback revived old endpoint")
				}
			}
		})
	}
}
