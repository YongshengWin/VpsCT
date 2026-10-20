package core

import (
	"strings"
	"testing"
)

func TestSingBoxPlatformSelectionKeepsOfficialReleaseBoundary(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		standard := officialSingBoxArchive("1.14.1", arch, false)
		musl := officialSingBoxArchive("1.14.1", arch, true)
		for _, alpine := range []bool{false, true} {
			got, err := selectSingBoxArchive(standard, "1.14.1", arch, alpine)
			want := standard
			if alpine {
				want = musl
			}
			if err != nil || got != want {
				t.Fatalf("platform selection: %q %v", got, err)
			}
		}
		if got, err := selectSingBoxArchive(musl, "1.14.1", arch, true); err != nil || got != musl {
			t.Fatal("explicit upstream musl URL rejected", err)
		}
		for _, wrong := range []string{
			strings.Replace(standard, "SagerNet/sing-box", "another/repository", 1),
			strings.Replace(standard, "v1.14.1/", "v1.14.2/", 1),
			standard + "?source=override",
			strings.Replace(standard, "https://", "http://", 1),
		} {
			if _, err := selectSingBoxArchive(wrong, "1.14.1", arch, true); err == nil {
				t.Fatal("accepted alternate executable source")
			}
		}
	}
	if _, err := selectSingBoxArchive("", "../1.14.1", "amd64", true); err == nil {
		t.Fatal("accepted unsafe version")
	}
}

func TestMuslChecksumIsSelectedWithoutChangingOtherHostsOrCores(t *testing.T) {
	sums := map[string]string{"amd64": "standard", "amd64-musl": "musl"}
	if got := platformCoreChecksum("sing-box", "amd64", true, sums); got != "musl" {
		t.Fatal("Alpine selected the standard archive checksum")
	}
	for _, tc := range []struct {
		name   string
		alpine bool
	}{
		{"sing-box", false}, {"mita", true}, {"snell-server", true},
	} {
		if got := platformCoreChecksum(tc.name, "amd64", tc.alpine, sums); got != "standard" {
			t.Fatal("musl checksum leaked into another archive")
		}
	}
	if got := platformCoreChecksum("sing-box", "amd64", true, map[string]string{"amd64": "explicit"}); got != "explicit" {
		t.Fatal("explicit standard checksum silently bypassed")
	}
}
