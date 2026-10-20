package maintenance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/safehttp"
)

func TestAgentVerificationUsesQueuedReleaseInsteadOfLatest(t *testing.T) {
	old := verifyAgentRelease
	t.Cleanup(func() { verifyAgentRelease = old })
	calls := 0
	verifyAgentRelease = func(_ context.Context, component, version string, payload io.ReadSeeker) error {
		calls++
		if component != "agent" || version != "v0.1.5" {
			return fmt.Errorf("wrong official checksum catalog: %s/%q", component, version)
		}
		// The controller still distributes A even though a later release B
		// exists. Only the explicitly queued A catalog is valid for these bytes.
		got, err := io.ReadAll(payload)
		if err != nil || string(got) != "release A" {
			return errors.New("release A checksum mismatch")
		}
		return nil
	}
	if err := verifyAgentPayload(context.Background(), "v0.1.5 (abcdef1)", strings.NewReader("release A")); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"", "dev", "v0.1.5/../../latest", "v0.1.5 unexpected"} {
		if err := verifyAgentPayload(context.Background(), version, strings.NewReader("release A")); err == nil {
			t.Fatalf("invalid release %q fell back to latest", version)
		}
	}
	if calls != 1 {
		t.Fatal("invalid release reached publisher verification", calls)
	}
}

func TestAgentDownloadRetryClassification(t *testing.T) {
	old := downloadAgentTo
	t.Cleanup(func() { downloadAgentTo = old })
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("valid artifact")))
	for _, tc := range []struct {
		name  string
		err   error
		body  string
		retry bool
	}{
		{"interrupted transfer", io.ErrUnexpectedEOF, "partial", true},
		{"network failure", errors.New("isolated transport failed"), "", true},
		{"size limit", safehttp.ErrSize, "", false},
		{"digest mismatch", nil, "wrong artifact", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			downloadAgentTo = func(_ context.Context, _ string, dst io.Writer) (int64, error) {
				n, _ := io.WriteString(dst, tc.body)
				return int64(n), tc.err
			}
			err := downloadAgent(context.Background(), "https://panel.example.test/dl/agent/linux-amd64", want, filepath.Join(t.TempDir(), "agent.new"))
			if err == nil {
				t.Fatal("incomplete or invalid artifact accepted")
			}
			var transient *agentDownloadError
			if errors.As(err, &transient) != tc.retry {
				t.Fatalf("unsafe retry classification: %v", err)
			}
		})
	}
}
