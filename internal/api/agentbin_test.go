package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeAgentArch(t *testing.T) {
	if got := normalizeAgentArch("x86_64"); got != "amd64" {
		t.Fatalf("x86_64: %s", got)
	}
	if got := normalizeAgentArch("aarch64"); got != "" {
		t.Fatalf("aarch64: %s", got)
	}
	if got := normalizeAgentArch("ppc64"); got != "" {
		t.Fatalf("ppc64: %s", got)
	}
}

func TestWithdrawnARMDistributionIsUnavailable(t *testing.T) {
	c := newTestAPI(t)
	dir := t.TempDir()
	c.api.Config.AgentBinDir = dir
	// An old file left on disk must not become a supported download or update.
	if err := os.WriteFile(filepath.Join(dir, "ctlvps-agent-linux-arm64"), []byte("old fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	c.do("GET", "/dl/agent/linux-arm64", nil, 404)
	if _, ok := c.api.agentBinaryMeta("arm64"); ok {
		t.Fatal("withdrawn ARM binary offered as update")
	}
}
