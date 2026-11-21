package subscription

import (
	"context"
	"path/filepath"
	"testing"

	"ctlvps/internal/store"
)

func TestSeedPreservesEditedBuiltinAndRestoresDefault(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	svc := NewService(st)
	if err := svc.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var originalID int64
	for _, item := range list {
		if item.IsBuiltin && item.Kind == "mihomo" {
			originalID = item.ID
			item.IsBuiltin = false
			item.Content = "mixed-port: 12345\n"
			if err := st.UpdateTemplate(ctx, &item); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if originalID == 0 {
		t.Fatal("default template missing")
	}
	if err := svc.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	custom, err := st.GetTemplate(ctx, originalID)
	if err != nil || custom.IsBuiltin || custom.Content != "mixed-port: 12345\n" {
		t.Fatal("customized template overwritten", err)
	}
	list, err = st.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.ID != originalID && item.IsBuiltin && item.Kind == "mihomo" {
			return
		}
	}
	t.Fatal("default template was not restored")
}
