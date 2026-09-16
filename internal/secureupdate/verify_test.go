package secureupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndependentTrustAndRollback(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if e := InitRepository(keys); e != nil {
		t.Fatal(e)
	}
	data := []byte("inert synthetic executable fixture")
	file := filepath.Join(dir, "fixture")
	if e := os.WriteFile(file, data, 0600); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(dir, "metadata")
	files := []ReleaseFile{{file, Identity{Product: "VpsCT", Component: "agent", Version: "v1.0.0", Arch: "amd64", Epoch: 2}}}
	if e := PublishRepository(keys, out, files, 1, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewTLSServer(http.FileServer(http.Dir(out)))
	defer srv.Close()
	root, _ := os.ReadFile(filepath.Join(keys, "root.json"))
	v := Verifier{Policy: Policy{MetadataURL: srv.URL}, Root: root, Dir: filepath.Join(dir, "cache"), Client: srv.Client()}
	if e := v.Verify(context.Background(), "agent", "v1.0.0", "amd64", data); e != nil {
		t.Fatal(e)
	}
	for _, f := range []struct {
		component, version, arch string
		data                     []byte
	}{{"agent", "v1.0.0", "amd64", []byte("tampered")}, {"controller", "v1.0.0", "amd64", data}, {"agent", "v0.9.0", "amd64", data}, {"agent", "v1.0.0", "arm64", data}} {
		if v.Verify(context.Background(), f.component, f.version, f.arch, f.data) == nil {
			t.Fatal("untrusted identity accepted")
		}
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if e := CheckRollback(v.Dir, v.Policy, "agent", "amd64", digest); e != nil {
		t.Fatal(e)
	}
	v.Policy.MinEpoch = map[string]int64{"agent": 3}
	if CheckRollback(v.Dir, v.Policy, "agent", "amd64", digest) == nil {
		t.Fatal("offline rollback crossed security floor")
	}
	if v.Verify(context.Background(), "agent", "v1.0.0", "amd64", data) == nil {
		t.Fatal("security rollback accepted")
	}
	other := filepath.Join(dir, "other")
	if e := InitRepository(other); e != nil {
		t.Fatal(e)
	}
	v.Root, _ = os.ReadFile(filepath.Join(other, "root.json"))
	v.Dir = filepath.Join(dir, "othercache")
	v.Policy.MinEpoch = nil
	if v.Verify(context.Background(), "agent", "v1.0.0", "amd64", data) == nil {
		t.Fatal("wrong publisher accepted")
	}
}

func TestRootRotationAndExpiry(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old")
	next := filepath.Join(dir, "next")
	if e := InitRepository(old); e != nil {
		t.Fatal(e)
	}
	if e := RotateRoot(old, next); e != nil {
		t.Fatal(e)
	}
	root, _ := os.ReadFile(filepath.Join(old, "root.json"))
	data := releaseArchive(t)
	file := filepath.Join(dir, "target")
	_ = os.WriteFile(file, data, 0600)
	files := []ReleaseFile{{file, Identity{Product: "VpsCT", Component: "controller", Version: "v2", Arch: "arm64", Epoch: 3}}}
	out := filepath.Join(dir, "metadata")
	if e := PublishRepository(next, out, files, 2, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewTLSServer(http.FileServer(http.Dir(out)))
	defer srv.Close()
	v := Verifier{Policy: Policy{MetadataURL: srv.URL}, Root: root, Dir: filepath.Join(dir, "cache"), Client: srv.Client()}
	if e := v.Verify(context.Background(), "controller", "v2", "arm64", data); e != nil {
		t.Fatal("dual-signed rotation", e)
	}
	ts, e := metadata.Timestamp().FromFile(filepath.Join(out, "timestamp.json"))
	if e != nil {
		t.Fatal(e)
	}
	ts.Signed.Version++
	ts.Signed.Expires = time.Now().Add(-time.Minute)
	ts.Signatures = nil
	signer, e := loadSigner(next, "timestamp")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ts.Sign(signer); e != nil {
		t.Fatal(e)
	}
	b, _ := ts.ToBytes(true)
	_ = os.WriteFile(filepath.Join(out, "timestamp.json"), b, 0644)
	if v.Verify(context.Background(), "controller", "v2", "arm64", data) == nil {
		t.Fatal("expired signed metadata accepted")
	}
}
