package store

import (
	"context"
	"testing"

	"ctlvps/internal/domain"
)

func TestSharePublicIDMigrationAndStability(t *testing.T) {
	ctx := context.Background()
	old, path := historicalStore(t, 48)
	_, err := old.db.Exec(`INSERT INTO shares(name,period_start,created_at,updated_at) VALUES('old','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := st.GetShare(ctx, 1)
	if err != nil || !validSharePublicID(legacy.PublicID) {
		t.Fatal("legacy share did not receive public ID", err)
	}
	second := domain.Share{Name: "new"}
	if err := st.CreateShare(ctx, &second); err != nil {
		t.Fatal(err)
	}
	if !validSharePublicID(second.PublicID) || second.PublicID == legacy.PublicID {
		t.Fatal("new share identifier invalid or duplicated")
	}
	if _, err := st.db.Exec(`UPDATE shares SET public_id=? WHERE id=?`, second.PublicID, legacy.ID); err == nil {
		t.Fatal("public ID was mutable")
	}
	if _, err := st.GetShareByPublicID(ctx, second.PublicID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetShareByPublicID(ctx, "1"); err != ErrNotFound {
		t.Fatal("numeric identifier accepted as public ID", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	loaded, err := st.GetShare(ctx, legacy.ID)
	if err != nil || loaded.PublicID != legacy.PublicID {
		t.Fatal("identifier changed after restart", err)
	}
}
