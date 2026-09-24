package desired

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/domain"
	"ctlvps/internal/networkconfig"
	"ctlvps/internal/provision"
	"ctlvps/internal/store"
)

func TestSlowPublishCannotReplaceNewerNetworkIntent(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "publish.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := domain.Server{Name: "fixture", Enabled: true, CoreMode: domain.CoreModeStable}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	n, err := provision.NewNode(server, "", provision.Options{Protocol: "ss", Port: 21001})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, &n); err != nil {
		t.Fatal(err)
	}
	b := New(s)
	first, _, err := b.Publish(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The payload is complete when the INSERT commits; no later UPDATE is
	// needed even if the database rejects all payload rewrites.
	if _, err := s.DB().Exec(`CREATE TRIGGER forbid_payload_rewrite BEFORE UPDATE OF payload ON desired_states BEGIN SELECT RAISE(ABORT,'immutable fixture'); END`); err != nil {
		t.Fatal(err)
	}
	oldBuild := New(s)
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	oldBuild.Now = func() time.Time {
		once.Do(func() { close(entered); <-resume })
		return time.Now().UTC()
	}
	type result struct {
		rec domain.DesiredState
		new bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		rec, created, err := oldBuild.Publish(ctx, server.ID)
		done <- result{rec, created, err}
	}()
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old build did not reach publication")
	}
	policy := &networkconfig.Node{ListenMode: "all", AdvertiseMode: "inherit", OnUnavailable: "block"}
	if _, err := s.SetNodeNetwork(ctx, n.ID, 0, policy, nil); err != nil {
		t.Fatal(err)
	}
	newer, created, err := b.Publish(ctx, server.ID)
	if err != nil || !created || newer.Revision != first.Revision+1 {
		t.Fatal("new network publish failed", err)
	}
	close(resume)
	select {
	case old := <-done:
		if old.err != nil || old.new || old.rec.Revision != newer.Revision || old.rec.Hash != newer.Hash {
			t.Fatal("slow publisher overwrote or failed to rebuild newer intent", old.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow publisher did not finish")
	}
	forced, created, err := b.ForcePublish(ctx, server.ID)
	if err != nil || !created || forced.Revision != newer.Revision+1 || forced.Hash != newer.Hash {
		t.Fatal("explicit retry lost atomic publication", err)
	}
	var ds agentproto.DesiredState
	if err := json.Unmarshal(forced.Payload, &ds); err != nil || ds.Revision != forced.Revision || ds.NetworkBindingVersion != agentproto.NetworkBindingVersion {
		t.Fatal("published incomplete payload", err)
	}
	if err := agentproto.ValidateDesired(&ds, server.ID, first.Revision, first.Hash); err != nil {
		t.Fatal("published payload fails agent verification", err)
	}
}
