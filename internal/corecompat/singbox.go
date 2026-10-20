// Package corecompat describes configuration compatibility with official cores.
// It never selects a newer version or distributes a locally modified executable.
package corecompat

import (
	"strconv"
	"strings"
)

// NetworkBaseline is the official release used by the network integration suite.
const NetworkBaseline = "1.14.1"

// ReleaseVersion accepts the official release naming scheme, including the
// upstream prereleases available to legacy configurations. Network capability
// predicates below deliberately require stable releases.
func ReleaseVersion(version string) bool {
	if strings.HasPrefix(version, "v") {
		return false
	}
	base, suffix, pre := strings.Cut(version, "-")
	if _, _, _, ok := StableVersion(base); !ok {
		return false
	}
	if !pre {
		return true
	}
	stage, number, ok := strings.Cut(suffix, ".")
	n, err := strconv.Atoi(number)
	return ok && (stage == "alpha" || stage == "beta" || stage == "rc") && err == nil && n >= 0 && strconv.Itoa(n) == number
}

// StableVersion rejects forks, prereleases, and malformed download path segments.
func StableVersion(version string) (major, minor, patch int, ok bool) {
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return
	}
	values := []*int{&major, &minor, &patch}
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 || strconv.Itoa(v) != part {
			return 0, 0, 0, false
		}
		*values[i] = v
	}
	return major, minor, patch, true
}

// ModernConfig uses the official 1.14 certificate-provider and DNS-cache schema.
func ModernConfig(version string) bool {
	major, minor, _, ok := StableVersion(version)
	return ok && major == 1 && minor >= 14
}

// NetworkBinding supports the maintained configuration families, including
// official patch upgrades. A new minor family needs configuration adaptation;
// accepting every future release would silently assume unchanged semantics.
func NetworkBinding(version string) bool {
	major, minor, patch, ok := StableVersion(version)
	return ok && major == 1 && ((minor == 12 && patch >= 14) || minor == 13 || minor == 14)
}

func SS2022Outbound(version string) bool {
	major, minor, patch, ok := StableVersion(version)
	return ok && major == 1 && minor == 14 && patch >= 1
}

// UDPForward requires the official Clash connection API plus the agent's
// independent kernel-admission reaper. Version alone never enables the feature.
func UDPForward(version string) bool {
	major, minor, patch, ok := StableVersion(version)
	return ok && major == 1 && minor == 14 && patch >= 1
}

const UDPForwardUnavailable = "UDP 固定转发需要官方 sing-box 1.14.1 或兼容的 1.14.x 稳定版，以及支持 UDP 会话管理的新 agent"
const SS2022Requirement = "SS-2022 出口需要在设置中选择官方 sing-box 1.14.1 或兼容的 1.14.x 稳定版"
