package nft

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"ctlvps/internal/agentproto"
)

// fakeIPRules installs an `ip` stub on PATH that prints the given JSON rule
// listing for every family, mirroring `ip -j rule show` output.
func fakeIPRules(t *testing.T, rules string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	script := "#!/bin/sh\necho '" + rules + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "ip"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

// tailscaled installs fwmark rules masked to bits 16-23 (0xff0000); they never
// route on the VpsCT accounting namespace (bits 24-31) and must not block
// every apply (issue #57).
func TestCheckMarkPrefixesSkipsForeignMaskWindows(t *testing.T) {
	fakeIPRules(t, `[{"priority":5210,"fwmark":"0x80000","fwmask":"0xff0000","table":"main"},{"priority":5230,"fwmark":"0x80000","fwmask":"0xff0000","table":"default"},{"priority":5250,"fwmark":"0x80000","fwmask":"0xff0000","action":"unreachable"},{"priority":5270,"table":"52"}]`)
	if err := checkMarkPrefixes(context.Background(), []uint32{MarkPrefix, agentproto.BootstrapMarkPrefix}); err != nil {
		t.Fatalf("tailscale-style rules rejected: %v", err)
	}
	// A mask inside the prefix window that requires a different top byte
	// never matches VpsCT marks either.
	fakeIPRules(t, `[{"priority":100,"fwmark":"0x00080000","fwmask":"0xff000000","table":"100"}]`)
	if err := checkMarkPrefixes(context.Background(), []uint32{MarkPrefix, agentproto.BootstrapMarkPrefix}); err != nil {
		t.Fatalf("non-matching prefix window rejected: %v", err)
	}
}

func TestCheckMarkPrefixesFlagsRealOverlaps(t *testing.T) {
	prefixes := []uint32{MarkPrefix, agentproto.BootstrapMarkPrefix, agentproto.ForwardMarkPrefix}
	for _, tc := range []struct {
		name  string
		rules string
	}{
		{"full mask", `[{"priority":100,"fwmark":"0x43000000","table":"100"}]`},
		{"masked prefix window", `[{"priority":100,"fwmark":"0x43080000","fwmask":"0xff000000","table":"100"}]`},
		{"partial nibble overlap", `[{"priority":100,"fwmark":"0x55000000","fwmask":"0x0f000000","table":"100"}]`},
	} {
		fakeIPRules(t, tc.rules)
		if err := checkMarkPrefixes(context.Background(), prefixes); err == nil {
			t.Fatalf("%s: overlap accepted", tc.name)
		}
	}
}

// FIB_RULE_INVERT rules match the complement of the fwmark class; with any
// mask bit set that complement also swallows VpsCT marks, so they stay
// conflicts. A zero mask matches every packet and its complement nothing.
func TestCheckMarkPrefixesConservativeForInvertedRules(t *testing.T) {
	fakeIPRules(t, `[{"priority":100,"not":null,"fwmark":"0x80000","fwmask":"0xff0000","table":"100"}]`)
	if err := checkMarkPrefixes(context.Background(), []uint32{MarkPrefix}); err == nil {
		t.Fatal("inverted fwmark rule accepted")
	}
	fakeIPRules(t, `[{"priority":100,"not":null,"fwmark":"0x0","fwmask":"0x0","table":"100"}]`)
	if err := checkMarkPrefixes(context.Background(), []uint32{MarkPrefix}); err != nil {
		t.Fatalf("degenerate inverted rule rejected: %v", err)
	}
}
