// Package buildinfo exposes version metadata injected at build time via
// -ldflags "-X ctlvps/internal/buildinfo.Version=vX.Y.Z".
package buildinfo

// Version is the semantic version of the current build. "dev" when built
// without ldflags.
var Version = "dev"

// Commit is the short git commit hash, when injected.
var Commit = ""

// String returns a human readable version string.
func String() string {
	if Commit == "" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
