package store

import (
	"bytes"
	"ctlvps/internal/domain"
	"testing"
)

func TestShareEntryMigrationPreservesLegacyIdentityAndUsage(t *testing.T) {
	ctx := t.Context()
	old, path := historicalStore(t, 51)
	srv := domain.Server{Name: "fixture", PublicHost: "test.invalid", CoreMode: domain.CoreModeStable, Enabled: true}
	historicalCreateServer(t, old, &srv)
	sh := domain.Share{Name: "legacy", Targets: []domain.ShareTarget{{ServerID: srv.ID, Protocols: []string{"ss"}, QuotaBytes: map[string]int64{"ss": 20}}}, UsedUpload: 18, UsedDownload: 3}
	if err := old.CreateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Name: "fixture", Token: "legacy-fixture-token", TokenHash: "fixturehash", ShareID: &sh.ID, Enabled: true}
	if err := old.CreateSubscription(ctx, &sub); err != nil {
		t.Fatal(err)
	}
	sh.SubscriptionID = &sub.ID
	if err := old.UpdateShare(ctx, &sh); err != nil {
		t.Fatal(err)
	}
	n := domain.Node{Name: "dedicated", Protocol: "ss", Source: domain.NodeDeployed, Server: srv.PublicHost, Port: 27299, ListenPort: 27299, ServerID: &srv.ID, ShareID: &sh.ID, Core: domain.CoreSingBox, Enabled: true, Params: []byte(`{"cipher":"aes-128-gcm","udp":true,"password":"fixture"}`), ServerParams: []byte(`{"method":"aes-128-gcm","password":"fixture"}`)}
	if err := old.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`UPDATE shares SET node_usage=json_object(cast(? AS text),json_array(18,3)) WHERE id=?`, n.ID, sh.ID); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	loaded, err := st.GetShare(ctx, sh.ID)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := st.GetNode(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := st.GetSubscriptionByShare(ctx, sh.ID)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := st.ShareEntryBindings(ctx, sh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 0 || len(loaded.Targets) != 1 || len(loaded.Targets[0].Entries) != 0 || loaded.PublicID != sh.PublicID || !loaded.NodeExhausted(ln) || loaded.UsedUpload != 18 || loaded.UsedDownload != 3 || ln.ID != n.ID || ln.ListenPort != n.ListenPort || ls.Token != sub.Token || ls.ID != sub.ID || !bytes.Equal(ln.Params, n.Params) || !bytes.Equal(ln.ServerParams, n.ServerParams) {
		t.Fatal("migration changed legacy identity, subscription, credentials or accounting")
	}
}
