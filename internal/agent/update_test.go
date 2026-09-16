package agent

import (
	"context"
	"crypto/sha256"
	"ctlvps/internal/safehttp"
	"ctlvps/internal/secureupdate"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(next)
	}))
	t.Cleanup(srv.Close)
	oldDownload := downloadSelf
	t.Cleanup(func() { downloadSelf = oldDownload })
	downloadSelf = func(ctx context.Context, raw string) ([]byte, error) {
		r, e := srv.Client().Get(raw)
		if e != nil {
			return nil, e
		}
		defer r.Body.Close()
		return safehttp.ReadBounded(r.Body, 128<<20)
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	keys := filepath.Join(dir, "keys")
	if e := secureupdate.InitRepository(keys); e != nil {
		t.Fatal(e)
	}
	artifact := filepath.Join(dir, "artifact")
	_ = os.WriteFile(artifact, next, 0600)
	md := filepath.Join(dir, "metadata")
	if e := secureupdate.PublishRepository(keys, md, []secureupdate.ReleaseFile{{Path: artifact, Identity: secureupdate.Identity{Product: "VpsCT", Component: "agent", Version: "v1.0.0", Arch: runtime.GOARCH, Epoch: 1}}}, 1, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	metadataServer := httptest.NewTLSServer(http.FileServer(http.Dir(md)))
	defer metadataServer.Close()
	root, _ := os.ReadFile(filepath.Join(keys, "root.json"))
	v := secureupdate.Verifier{Policy: secureupdate.Policy{MetadataURL: metadataServer.URL}, Root: root, Dir: filepath.Join(dir, "cache"), Client: metadataServer.Client()}
	oldVerifier := verifyRelease
	verifyRelease = func(ctx context.Context, component, version string, b []byte) error {
		return v.Verify(ctx, component, version, runtime.GOARCH, b)
	}
	t.Cleanup(func() { verifyRelease = oldVerifier })

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
