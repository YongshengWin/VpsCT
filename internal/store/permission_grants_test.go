package store

import (
	"context"
	"errors"
	"testing"

	"ctlvps/internal/authorization"
	"ctlvps/internal/domain"
)

func TestPermissionGrantDelegationAndRevocation(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	owner := domain.User{Username: "grant-admin", PasswordHash: "hash", Enabled: true, Role: domain.RoleAdmin}
	operator := domain.User{Username: "grant-operator", PasswordHash: "hash", Enabled: true}
	member := domain.User{Username: "grant-member", PasswordHash: "hash", Enabled: true}
	for _, user := range []*domain.User{&owner, &operator, &member} {
		if err := st.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	server := domain.Server{Name: "grant-server", Enabled: true}
	if err := st.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	all := authorization.Scope{Kind: "all"}
	scope := authorization.Scope{Kind: "servers", IDs: []int64{server.ID}}
	view := authorization.Grant{UserID: operator.ID, Permission: "shares.view", Scope: scope}
	delegate := authorization.Grant{UserID: operator.ID, Permission: authorization.Delegate, Scope: all}
	accountEdit := authorization.Grant{UserID: operator.ID, Permission: "accounts.edit", Scope: all}
	for _, grant := range []*authorization.Grant{&view, &delegate, &accountEdit} {
		if err := st.SavePermissionGrant(ctx, owner, grant, domain.AuditEvent{}); err != nil {
			t.Fatal(err)
		}
	}
	operator, _ = st.GetUser(ctx, operator.ID)
	child := authorization.Grant{UserID: member.ID, Permission: "shares.view", Scope: scope}
	if err := st.SavePermissionGrant(ctx, operator, &child, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	e, err := st.PermissionSnapshot(ctx)
	if err != nil || !e.Allows(member.ID, "shares.view", scope) {
		t.Fatal("valid delegation absent", err)
	}
	escalation := authorization.Grant{UserID: member.ID, Permission: "servers.edit", Scope: all}
	if err := st.SavePermissionGrant(ctx, operator, &escalation, domain.AuditEvent{}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("expanded grant accepted", err)
	}
	memberBefore, _ := st.GetUser(ctx, member.ID)
	if err := st.RevokePermissionGrant(ctx, owner, view.ID, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	e, err = st.PermissionSnapshot(ctx)
	if err != nil || e.Allows(member.ID, "shares.view", scope) {
		t.Fatal("revoked parent still authorized descendant", err)
	}
	memberAfter, _ := st.GetUser(ctx, member.ID)
	if memberAfter.SecurityVersion <= memberBefore.SecurityVersion {
		t.Fatal("descendant credentials not invalidated")
	}
	var audits int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('permission.grant','permission.revoke')`).Scan(&audits); err != nil || audits != 5 {
		t.Fatal("missing transactional authorization audit", audits, err)
	}
}
