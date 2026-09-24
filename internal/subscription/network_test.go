package subscription

import (
	"testing"

	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
)

func TestProxyAddressInheritanceAndOverride(t *testing.T) {
	id := int64(1)
	n := domain.Node{ServerID: &id, Server: "old.example.test", Port: 443}
	hosts := map[int64]string{1: "new.example.test"}
	if ProxyFor(n, hosts).Server != n.Server {
		t.Fatal("legacy address semantics changed")
	}
	n.Network = &networkconfig.Node{AdvertiseMode: "inherit"}
	if ProxyFor(n, hosts).Server != hosts[1] {
		t.Fatal("explicit inheritance used stale stored address")
	}
	n.Network.AdvertiseMode = "override"
	if ProxyFor(n, hosts).Server != n.Server {
		t.Fatal("override followed server address")
	}
	n.Server = ""
	if ProxyFor(n, hosts).Server != "" {
		t.Fatal("invalid override silently inherited another address")
	}
}
