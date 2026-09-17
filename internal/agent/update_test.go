package agent

import (
	"context"
	"crypto/sha256"
	"ctlvps/internal/safehttp"
	"ctlvps/internal/secureupdate"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	downloadSelf = func(ctx context.Context, raw string, dst io.Writer) (int64, error) {
		r, e := srv.Client().Get(raw)
		if e != nil {
			return 0, e
		}
		defer r.Body.Close()
		return safehttp.CopyBounded(dst, r.Body, 128<<20)
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
	verifyRelease = func(ctx context.Context, component, version string, b io.ReadSeeker) error {
		return v.VerifyReader(ctx, component, version, runtime.GOARCH, b)
	}
	t.Cleanup(func() { verifyRelease = oldVerifier })

	old := executablePath
	executablePath = func() string { return target }
	t.Cleanup(func() { executablePath = old })

	if err := applySelfUpdateInline(context.Background(), srv.URL, agentproto.AgentUpdateSpec{
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

func TestFailedStreamingUpdatePreservesExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent")
	os.WriteFile(target, []byte("old"), 0755)
	oldPath, oldDownload, oldVerify := executablePath, downloadSelf, verifyRelease
	t.Cleanup(func() { executablePath = oldPath; downloadSelf = oldDownload; verifyRelease = oldVerify })
	executablePath = func() string { return target }
	next := []byte("new")
	sum := sha256.Sum256(next)
	for _, failure := range []string{"download", "checksum", "trust"} {
		t.Run(failure, func(t *testing.T) {
			downloadSelf = func(_ context.Context, _ string, dst io.Writer) (int64, error) {
				n, err := dst.Write(next)
				if failure == "download" {
					err = io.ErrUnexpectedEOF
				}
				return int64(n), err
			}
			verifyRelease = func(context.Context, string, string, io.ReadSeeker) error {
				if failure == "trust" {
					return io.ErrUnexpectedEOF
				}
				return nil
			}
			digest := hex.EncodeToString(sum[:])
			if failure == "checksum" {
				digest = strings.Repeat("0", 64)
			}
			if err := applySelfUpdateInline(context.Background(), "https://example.test", agentproto.AgentUpdateSpec{SHA256: digest, URL: "/dl/agent/linux-fixture"}); err == nil {
				t.Fatal("failure accepted")
			}
			got, _ := os.ReadFile(target)
			if string(got) != "old" {
				t.Fatal("running executable replaced")
			}
			files, _ := filepath.Glob(filepath.Join(dir, ".agent-update-*"))
			if len(files) != 0 {
				t.Fatal("partial staging leaked", files)
			}
		})
	}
}
