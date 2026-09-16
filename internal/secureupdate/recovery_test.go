package secureupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func releaseArchive(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	data := []byte("inert controller")
	if e := w.WriteHeader(&tar.Header{Name: "ctlvpsd", Mode: 0755, Size: int64(len(data))}); e != nil {
		t.Fatal(e)
	}
	w.Write(data)
	w.Close()
	z.Close()
	return b.Bytes()
}
func TestRecoveryChecksInstalledBytes(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "release")
	os.Mkdir(root, 0700)
	archive := releaseArchive(t)
	digest := fmt.Sprintf("%x", sha256.Sum256(archive))
	if e := recordArchive(dir, digest, archive); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(root, "ctlvpsd")
	os.WriteFile(p, []byte("inert controller"), 0700)
	if e := CheckInstalled(dir, digest, root); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(p, []byte("evil! controller"), 0700)
	if CheckInstalled(dir, digest, root) == nil {
		t.Fatal("tampered installed binary accepted")
	}
	os.Remove(p)
	os.Symlink(filepath.Join(dir, "other"), p)
	if CheckInstalled(dir, digest, root) == nil {
		t.Fatal("symlink accepted")
	}
}
func TestTransactionExpiryDoesNotOverrideRevocation(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "verified"), 0700)
	data := []byte("agent fixture")
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	source := filepath.Join(dir, "previous")
	os.WriteFile(source, data, 0700)
	id, _ := json.Marshal(Identity{"VpsCT", "agent", "v1", "arm64", 1})
	os.WriteFile(filepath.Join(dir, "verified", digest+".json"), id, 0600)
	os.WriteFile(filepath.Join(dir, "epochs.json"), []byte(`{"agent":1}`), 0600)
	rp := ReleasePolicy{1, "VpsCT", 1, time.Now().Add(time.Hour), map[string]int64{}, nil}
	if e := acceptReleasePolicy(dir, rp); e != nil {
		t.Fatal(e)
	}
	r, e := PrepareRecovery(dir, Policy{}, "agent", "arm64", digest, source)
	if e != nil {
		t.Fatal(e)
	}
	rp.Expires = time.Now().Add(-time.Second)
	b, _ := json.Marshal(rp)
	os.WriteFile(filepath.Join(dir, "security-policy.json"), b, 0600)
	if _, e = PrepareRecovery(dir, Policy{}, "agent", "arm64", digest, source); e == nil {
		t.Fatal("expired policy began new transaction")
	}
	if e = r.Check(dir, Policy{}); e != nil {
		t.Fatal("in-flight recovery blocked by policy expiry", e)
	}
	rp.Sequence++
	rp.Expires = time.Now().Add(time.Hour)
	rp.Revoked = []string{digest}
	if e = acceptReleasePolicy(dir, rp); e != nil {
		t.Fatal(e)
	}
	if r.Check(dir, Policy{}) == nil {
		t.Fatal("transaction ignored newer revocation")
	}
}
