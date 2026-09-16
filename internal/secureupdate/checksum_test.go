package secureupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestChecksumCatalog(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, sums string
		valid      bool
	}{
		{"valid", digest + "  ctlvps-agent-linux-amd64\n", true},
		{"missing", digest + "  other\n", false},
		{"duplicate", strings.Repeat(digest+"  ctlvps-agent-linux-amd64\n", 2), false},
		{"mismatch", strings.Repeat("b", 64) + "  ctlvps-agent-linux-amd64\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := matchChecksum([]byte(tc.sums), "ctlvps-agent-linux-amd64", digest)
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestChecksumRecoveryNoPublisherState(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "agent.previous")
	data := []byte("previous running binary")
	if err := os.WriteFile(source, data, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	r, err := PrepareRecovery(dir, defaultPolicy(), "agent", runtime.GOARCH, hex.EncodeToString(sum[:]), source)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Check(dir, defaultPolicy()); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = r.Check(dir, defaultPolicy()); err == nil {
		t.Fatal("changed recovery accepted")
	}
	r.Deadline = time.Now().Add(-time.Second)
	if err = r.Check(dir, defaultPolicy()); err == nil {
		t.Fatal("expired recovery accepted")
	}
}
