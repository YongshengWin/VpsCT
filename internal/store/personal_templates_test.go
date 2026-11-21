package store

import (
	"context"
	"errors"
	"testing"

	"ctlvps/internal/domain"
)

func TestPersonalTemplateShareOwnershipAndTransfer(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	a := domain.User{Username: "personal-a", PasswordHash: "hash", Enabled: true}
	b := domain.User{Username: "personal-b", PasswordHash: "hash", Enabled: true}
	for _, u := range []*domain.User{&a, &b} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	sh := domain.Share{Name: "personal-share", UserID: &a.ID}
	if err := st.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	tpl := domain.RuleTemplate{Name: "personal", Kind: "mihomo", Content: "private"}
	if err := st.CreatePersonalTemplate(ctx, a.ID, &tpl, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePersonalTemplate(ctx, a.ID, &domain.RuleTemplate{Name: "extra", Kind: "mihomo"}, 1); err == nil {
		t.Fatal("template limit not enforced")
	}
	list, err := st.ListTemplates(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("private template leaked to system list: %v %v", list, err)
	}
	if err := st.SetSharePersonalTemplate(ctx, sh.ID, b.ID, tpl.ID, "mihomo", tpl.UpdatedAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user could select template: %v", err)
	}
	if err := st.SetSharePersonalTemplate(ctx, sh.ID, a.ID, tpl.ID, "surge", tpl.UpdatedAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong format accepted: %v", err)
	}
	if err := st.SetSharePersonalTemplate(ctx, sh.ID, a.ID, tpl.ID, "mihomo", tpl.UpdatedAt.Add(-1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale template validation accepted: %v", err)
	}
	if err := st.SetSharePersonalTemplate(ctx, sh.ID, a.ID, tpl.ID, "mihomo", tpl.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSharePersonalTemplate(ctx, sh.ID, "mihomo")
	if err != nil || got.ID != tpl.ID || got.Content != tpl.Content {
		t.Fatalf("selected template missing: %v %v", got, err)
	}
	changed := tpl
	changed.Kind = "surge"
	if err := st.UpdateTemplate(ctx, &changed); err == nil {
		t.Fatal("changed kind of selected template")
	}
	sh.UserID = &b.ID
	if err := st.UpdateShareSettings(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSharePersonalTemplate(ctx, sh.ID, "mihomo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transfer retained previous selection: %v", err)
	}
	if err := st.SetSharePersonalTemplate(ctx, sh.ID, a.ID, tpl.ID, "mihomo", tpl.UpdatedAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("previous owner restored selection: %v", err)
	}
	if err := st.DeleteUser(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetTemplate(ctx, tpl.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted owner's template remained: %v", err)
	}
}
