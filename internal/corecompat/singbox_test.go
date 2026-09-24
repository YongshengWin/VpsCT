package corecompat

import "testing"

func TestReleaseNamesExcludeCustomBuilds(t *testing.T) {
	for _, version := range []string{"1.14.1", "1.15.0-alpha.6", "1.14.0-beta.1", "1.14.0-rc.1"} {
		if !ReleaseVersion(version) {
			t.Fatal("official release name rejected", version)
		}
	}
	for _, version := range []string{"1.14.1-ctlvps.1", "1.14.1-custom", "1.14.1+local", "1.14.1/other", "v1.14.1", "1.15.0-alpha.6/other"} {
		if ReleaseVersion(version) {
			t.Fatal("non-official name accepted", version)
		}
	}
}

func TestOfficialCompatibilityFamilies(t *testing.T) {
	for _, tc := range []struct {
		version     string
		binding, ss bool
	}{
		{"1.12.13", false, false}, {"1.12.14", true, false}, {"1.12.18", true, false},
		{"1.13.21", true, false}, {"1.14.0", true, false}, {"1.14.1", true, true},
		{"v1.14.2", true, true}, {"1.14.10", true, true}, {"1.15.0", false, false},
		{"1.14.1-ctlvps.1", false, false}, {"1.14.1-beta.1", false, false},
		{"1.14.1/other", false, false}, {"01.14.1", false, false}, {"", false, false},
	} {
		if NetworkBinding(tc.version) != tc.binding || SS2022Outbound(tc.version) != tc.ss {
			t.Errorf("wrong compatibility for %q", tc.version)
		}
		if UDPForward(tc.version) {
			t.Fatalf("UDP qualification failure bypassed by %q", tc.version)
		}
	}
}
