package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ctlvps/internal/agentproto"
)

func TestApplySelfUpdate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ctlvps-agent")
	if err := os.WriteFile(target, []byte("old-bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	next := []byte("new-agent-binary")
	sum := sha256.Sum256(next)
	sha := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(next)
	}))
	t.Cleanup(srv.Close)

	old := executablePath
	executablePath = func() string { return target }
	t.Cleanup(func() { executablePath = old })

	if err := applySelfUpdate(context.Background(), srv.URL, agentproto.AgentUpdateSpec{
		SHA256: sha, URL: "/agent",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := fileSHA256(target)
	if err != nil || got != sha {
		t.Fatalf("updated hash %s %v", got, err)
	}
	if resolveUpdateURL("https://panel.example", "/dl/agent/linux-amd64") != "https://panel.example/dl/agent/linux-amd64" {
		t.Fatal("resolve")
	}
}
