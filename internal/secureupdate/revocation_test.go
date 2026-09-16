package secureupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPolicyCannotForgetRevocation(t *testing.T) {
	dir := t.TempDir()
	p := ReleasePolicy{1, "VpsCT", 1, time.Now().Add(time.Hour), map[string]int64{"agent": 2}, []string{strings.Repeat("a", 64)}}
	if e := acceptReleasePolicy(dir, p); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*ReleasePolicy){func(p *ReleasePolicy) { p.Sequence = 0 }, func(p *ReleasePolicy) { p.Revoked = nil; p.Sequence++ }, func(p *ReleasePolicy) { p.MinEpoch = map[string]int64{}; p.Sequence++ }, func(p *ReleasePolicy) { p.Expires = time.Now().Add(-time.Second); p.Sequence++ }} {
		q := p
		change(&q)
		if acceptReleasePolicy(dir, q) == nil {
			t.Fatal("unsafe policy accepted")
		}
	}
}
func TestSignedRevocationPreventsInstallAndOfflineRollback(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if e := InitRepository(keys); e != nil {
		t.Fatal(e)
	}
	data := []byte("synthetic release")
	file := filepath.Join(dir, "file")
	os.WriteFile(file, data, 0600)
	files := []ReleaseFile{{file, Identity{"VpsCT", "agent", "v1", "amd64", 1}}}
	out := filepath.Join(dir, "metadata")
	if e := PublishRepository(keys, out, files, 1, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewTLSServer(http.FileServer(http.Dir(out)))
	defer srv.Close()
	root, _ := os.ReadFile(filepath.Join(keys, "root.json"))
	v := Verifier{Policy: Policy{MetadataURL: srv.URL}, Root: root, Dir: filepath.Join(dir, "cache"), Client: srv.Client()}
	if e := v.Verify(context.Background(), "agent", "v1", "amd64", data); e != nil {
		t.Fatal(e)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if e := CheckRollback(v.Dir, v.Policy, "agent", "amd64", digest); e != nil {
		t.Fatal(e)
	}
	p := ReleasePolicy{1, "VpsCT", 2, time.Now().Add(time.Hour), map[string]int64{}, []string{digest}}
	b, _ := json.Marshal(p)
	os.WriteFile(filepath.Join(keys, "security-policy.json"), b, 0600)
	next := filepath.Join(dir, "next")
	if e := PublishRepository(keys, next, files, 2, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	os.RemoveAll(out)
	os.Rename(next, out)
	if v.Verify(context.Background(), "agent", "v1", "amd64", data) == nil {
		t.Fatal("revoked target installed")
	}
	srv.Close()
	if CheckRollback(v.Dir, v.Policy, "agent", "amd64", digest) == nil {
		t.Fatal("revocation did not survive offline restart")
	}
}
func TestRootThresholdRotation(t *testing.T) {
	old := filepath.Join(t.TempDir(), "old")
	if e := InitRepository(old); e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(old, "root-3.key"))
	if e := RotateRoot(old, filepath.Join(t.TempDir(), "next")); e != nil {
		t.Fatal("two keys must rotate", e)
	}
	os.Remove(filepath.Join(old, "root-2.key"))
	if RotateRoot(old, filepath.Join(t.TempDir(), "bad")) == nil {
		t.Fatal("single root key rotated 2-of-3 root")
	}
}
