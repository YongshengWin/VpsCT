package store

import (
	"context"
	"strings"
	"testing"

	"ctlvps/internal/domain"
)

func TestTemplateVersionsAreEncryptedAndBounded(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	tpl := domain.RuleTemplate{Name: "fixture", Kind: "mihomo", Content: "initial private content"}
	if err := s.CreateTemplate(ctx, &tpl); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 35; i++ {
		tpl.Content = "changed"
		if err := s.UpdateTemplate(ctx, &tpl); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := s.TemplateVersions(ctx, tpl.ID)
	if err != nil || len(versions) != 30 {
		t.Fatalf("history cap: %d %v", len(versions), err)
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT content FROM rule_template_versions WHERE template_id=? ORDER BY id DESC LIMIT 1`, tpl.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, secretPrefix) || strings.Contains(raw, "changed") {
		t.Fatal("history stored plaintext")
	}
	if versions[0].Content != "changed" {
		t.Fatal("history could not be decrypted")
	}
}

func TestTemplateVersionsEncryptLegacySnapshotsOnUpgrade(t *testing.T) {
	s, path := historicalStore(t, 46)
	_, err := s.db.Exec(`INSERT INTO rule_templates(name,kind,description,content,variables,is_builtin,created_at,updated_at) VALUES('legacy','mihomo','','legacy-private-template','{}',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	var count int
	if err := upgraded.db.QueryRow(`SELECT count(*) FROM rule_template_versions WHERE content LIKE '%legacy-private-template%' OR variables='{}'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("legacy version history contains plaintext")
	}
}
