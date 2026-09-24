package netinventory

import (
	"context"
	"testing"
	"time"

	"ctlvps/internal/networkconfig"
)

func TestPrivateLiteralAndBootstrapUseSeparateLocalGrants(t *testing.T) {
	n, outer, snapshot := bindingFixture()
	cfg := networkconfig.SOCKS5{Server: "10.23.0.2", ServerPort: 1080, Authentication: "none", Family: "dual", DNS: outer.DNS, Outer: outer, ConnectTimeoutSeconds: 10}
	grant := networkconfig.TransportGrant{NodeID: 11, EgressProfileID: n.EgressProfileID, Purpose: "socks5", Network: "tcp", Address: cfg.Server, Port: cfg.ServerPort}
	resolve := func(grants []networkconfig.TransportGrant) error {
		_, err := ResolveSOCKS5WithGrants(context.Background(), 11, n, cfg, snapshot, false, time.Now(), grants)
		return err
	}
	if resolve(nil) == nil {
		t.Fatal("private transport accepted without a grant")
	}
	if err := resolve([]networkconfig.TransportGrant{grant}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*networkconfig.TransportGrant){
		func(g *networkconfig.TransportGrant) { g.NodeID++ },
		func(g *networkconfig.TransportGrant) { g.EgressProfileID++ },
		func(g *networkconfig.TransportGrant) { g.Purpose = "bootstrap_dns" },
		func(g *networkconfig.TransportGrant) { g.Network = "udp" },
		func(g *networkconfig.TransportGrant) { g.Port++ },
	} {
		wrong := grant
		mutate(&wrong)
		if resolve([]networkconfig.TransportGrant{wrong}) == nil {
			t.Fatal("unrelated root grant authorized a private endpoint")
		}
	}
	resolver := networkconfig.Resolver{Transport: "udp", Address: cfg.Server, Port: 1080}
	if ValidateBootstrapEndpoint(11, n.EgressProfileID, resolver, snapshot, []networkconfig.TransportGrant{grant}) == nil {
		t.Fatal("SOCKS grant authorized startup DNS")
	}
	grant.Purpose = "bootstrap_dns"
	if ValidateBootstrapEndpoint(11, n.EgressProfileID, resolver, snapshot, []networkconfig.TransportGrant{grant}) == nil {
		t.Fatal("UDP DNS did not need a UDP grant")
	}
	udp := grant
	udp.Network = "udp"
	grants := []networkconfig.TransportGrant{grant, udp}
	if err := ValidateBootstrapEndpoint(11, n.EgressProfileID, resolver, snapshot, grants); err != nil {
		t.Fatal(err)
	}
	if ValidateBootstrapEndpoint(11, n.EgressProfileID, resolver, snapshot, grants[1:]) == nil {
		t.Fatal("UDP DNS omitted its mandatory TCP fallback authorization")
	}
	snapshot.Interfaces[0].Addresses = append(snapshot.Interfaces[0].Addresses, cfg.Server+"/24")
	if ValidateBootstrapEndpoint(11, n.EgressProfileID, resolver, snapshot, grants) == nil {
		t.Fatal("authorized startup DNS became a local service")
	}
	grant.Purpose = "socks5"
	if resolve([]networkconfig.TransportGrant{grant}) == nil {
		t.Fatal("authorized SOCKS endpoint became a local service")
	}
}
