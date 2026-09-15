package api

import "testing"

func TestNormalizeAgentArch(t *testing.T) {
	if got := normalizeAgentArch("x86_64"); got != "amd64" {
		t.Fatalf("x86_64: %s", got)
	}
	if got := normalizeAgentArch("aarch64"); got != "arm64" {
		t.Fatalf("aarch64: %s", got)
	}
	if got := normalizeAgentArch("ppc64"); got != "" {
		t.Fatalf("ppc64: %s", got)
	}
}
