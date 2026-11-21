package authorization

import "testing"

func TestDelegationLosesAuthorityWithGrantorOrScope(t *testing.T) {
	all := Scope{Kind: "all"}
	servers := Scope{Kind: "servers", IDs: []int64{3, 5}}
	one := Scope{Kind: "servers", IDs: []int64{3}}
	e := Evaluator{Principals: map[int64]Principal{
		1: {ID: 1, Enabled: true, Admin: true},
		2: {ID: 2, Enabled: true},
		3: {ID: 3, Enabled: true},
	}, Grants: []Grant{
		{ID: 1, UserID: 2, GrantedBy: 1, Permission: "shares.edit", Scope: servers},
		{ID: 2, UserID: 2, GrantedBy: 1, Permission: Delegate, Scope: all},
		{ID: 3, UserID: 3, GrantedBy: 2, Permission: "shares.edit", Scope: one},
		{ID: 5, UserID: 2, GrantedBy: 1, Permission: "accounts.edit", Scope: all},
	}}
	if !e.Allows(3, "shares.edit", one) {
		t.Fatal("valid delegation was denied")
	}
	if e.Allows(3, "shares.edit", servers) || e.Allows(3, "shares.edit", Scope{Kind: "shares", IDs: []int64{3}}) || e.Allows(3, "servers.edit", one) {
		t.Fatal("scope or operation was expanded")
	}
	e.Grants[0].Scope = Scope{Kind: "servers", IDs: []int64{5}}
	if e.Allows(3, "shares.edit", one) {
		t.Fatal("delegation survived grantor's scope reduction")
	}
	e.Grants[0].Scope = servers
	e.Principals[2] = Principal{ID: 2, Enabled: false}
	if e.Allows(3, "shares.edit", one) {
		t.Fatal("delegation survived grantor disablement")
	}
	e.Principals[2] = Principal{ID: 2, Enabled: true}
	e.Grants[1].Permission = "accounts.view"
	if e.Allows(3, "shares.edit", one) {
		t.Fatal("delegation survived loss of delegation permission")
	}
}

func TestPermissionCyclesCannotCreateAuthority(t *testing.T) {
	all := Scope{Kind: "all"}
	e := Evaluator{Principals: map[int64]Principal{2: {ID: 2, Enabled: true}, 3: {ID: 3, Enabled: true}}, Grants: []Grant{
		{ID: 1, UserID: 2, GrantedBy: 3, Permission: "servers.view", Scope: all},
		{ID: 2, UserID: 3, GrantedBy: 2, Permission: "servers.view", Scope: all},
		{ID: 3, UserID: 2, GrantedBy: 3, Permission: Delegate, Scope: all},
		{ID: 4, UserID: 3, GrantedBy: 2, Permission: Delegate, Scope: all},
	}}
	if e.Allows(2, "servers.view", all) || e.Allows(3, Delegate, all) {
		t.Fatal("cyclic delegation created authority")
	}
}

func TestInvalidScopesFailClosed(t *testing.T) {
	all := Scope{Kind: "all"}
	for _, scope := range []Scope{{Kind: "all", IDs: []int64{1}}, {Kind: "shares"}, {Kind: "servers", IDs: []int64{1, 1}}, {Kind: "servers", IDs: []int64{0}}, {Kind: "unknown"}} {
		if all.Covers(scope) {
			t.Fatalf("accepted invalid scope: %+v", scope)
		}
	}
}
