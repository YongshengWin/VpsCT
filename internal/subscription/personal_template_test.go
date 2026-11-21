package subscription

import (
	"context"
	"strings"
	"testing"

	"ctlvps/internal/domain"
)

func TestSharePersonalTemplateFormatAndTransfer(t *testing.T) {
	st, svc, _, _ := applicationFixture(t)
	ctx := context.Background()
	a := domain.User{Username: "template-owner", PasswordHash: "hash", Enabled: true}
	b := domain.User{Username: "next-owner", PasswordHash: "hash", Enabled: true}
	for _, u := range []*domain.User{&a, &b} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	sh := domain.Share{Name: "personal-template", UserID: &a.ID}
	if err := st.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	tpl := domain.RuleTemplate{Name: "mine", Kind: "mihomo", Content: "mixed-port: 18080\nproxies: []\nproxy-groups: []\nrules: []\n"}
	if err := st.CreatePersonalTemplate(ctx, a.ID, &tpl, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSharePersonalTemplate(ctx, sh.ID, a.ID, tpl.ID, "mihomo", tpl.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Name: sh.Name, Kind: domain.SubShare, ShareID: &sh.ID}
	rendered, _, err := svc.Render(ctx, sub, "clash")
	if err != nil || !strings.Contains(string(rendered.Body), "18080") {
		t.Fatalf("personal template not rendered for format alias: %v", err)
	}
	_, bundle, err := svc.Render(ctx, sub, "surge")
	if err != nil || bundle.Template != nil {
		t.Fatalf("personal template affected another format: %v", err)
	}
	sh.UserID = &b.ID
	if err := st.UpdateShareSettings(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	_, bundle, err = svc.Render(ctx, sub, "mihomo")
	if err != nil || bundle.Template != nil {
		t.Fatalf("personal template remained after transfer: %v", err)
	}
}
