package networkconfig

import "testing"

func TestForwardPrivateAuthorityIsResourceAndDestinationScoped(t *testing.T) {
	g := ForwardGrant{ForwardID: 7, EgressProfileID: 3, Address: "10.42.0.0/24", Port: 443}
	p := ResolvedForwardTarget{ResolvedSOCKS5: ResolvedSOCKS5{Address: "10.42.0.2", Port: 443}}
	if _, err := p.WithGrants(7, 3, nil); err == nil {
		t.Fatal("missing grant accepted")
	}
	approved, err := p.WithGrants(7, 3, []ForwardGrant{g})
	if err != nil || approved.ValidateForForward(7, 3) != nil {
		t.Fatal(err)
	}
	if approved.ValidateForForward(8, 3) == nil || approved.ValidateForForward(7, 4) == nil {
		t.Fatal("another resource borrowed authority")
	}
	if approved.StillAuthorized(nil) {
		t.Fatal("removed grant remains authorized")
	}
	for _, address := range []string{"10.43.0.2", "127.0.0.1", "169.254.169.254", "64:ff9b::a2a:2"} {
		other := p
		other.Address = address
		if _, err := other.WithGrants(7, 3, []ForwardGrant{g}); err == nil {
			t.Fatal("scope escaped", address)
		}
	}
	p.Port++
	if _, err := p.WithGrants(7, 3, []ForwardGrant{g}); err == nil {
		t.Fatal("port escaped")
	}
	// Embedding DNS metadata must not let a node transport use forward grants.
	if approved.ResolvedSOCKS5.Validate() == nil {
		t.Fatal("forward permission became SOCKS permission")
	}
}
