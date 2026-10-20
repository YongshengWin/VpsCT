package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightFailureIsSanitized(t *testing.T) {
	for _, tc := range []struct{ output, code string }{
		{"fixture-private-config [proxy_mark_unavailable] fixture-private-config", "proxy_mark_unavailable"},
		{"[proxy_sandbox_unavailable] fixture-private-config", "proxy_sandbox_unavailable"},
		{"invalid setting fixture-private-config", "proxy_preflight_failed"},
	} {
		err := proxyPreflightError(tc.output)
		if !strings.Contains(err.Error(), "["+tc.code+"]") || strings.Contains(err.Error(), "fixture-private-config") {
			t.Fatal("unclassified or raw output exposed", err)
		}
	}
}

func TestOpenRCPreflightReportsSanitizedFailure(t *testing.T) {
	// Rejected before installing a logical unit: no OpenRC daemon or root
	// privilege is needed to verify the shared reporting boundary.
	sd := &Systemd{openRC: true}
	err := sd.CheckProxy(context.Background(), "/fixture-private-config", "public", nil)
	if err == nil || !strings.Contains(err.Error(), "[proxy_preflight_failed]") || strings.Contains(err.Error(), "fixture-private-config") {
		t.Fatalf("OpenRC error was not classified and sanitized: %v", err)
	}
}

func TestPreflightUsesOnlyNewInvocation(t *testing.T) {
	const oldID = "11111111111111111111111111111111"
	const newID = "22222222222222222222222222222222"
	for _, tc := range []struct{ name, id, want string }{
		{"new invocation", newID, "proxy_sandbox_unavailable"},
		{"old invocation", oldID, "proxy_preflight_failed"},
		{"missing id", "", "proxy_preflight_failed"},
		{"invalid id", "invalid", "proxy_preflight_failed"},
		{"zero id", strings.Repeat("0", 32), "proxy_preflight_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("PREFLIGHT_FIXTURE_STARTED", filepath.Join(dir, "started"))
			t.Setenv("PREFLIGHT_FIXTURE_OLD", oldID)
			t.Setenv("PREFLIGHT_FIXTURE_NEW", tc.id)
			t.Setenv("PREFLIGHT_FIXTURE_JOURNAL", filepath.Join(dir, "journal-called"))
			systemctl := `#!/bin/sh
case "$1" in
 show) if [ -f "$PREFLIGHT_FIXTURE_STARTED" ]; then printf '%s\n' "$PREFLIGHT_FIXTURE_NEW"; else printf '%s\n' "$PREFLIGHT_FIXTURE_OLD"; fi;;
 start) : > "$PREFLIGHT_FIXTURE_STARTED"; exit 1;;
 *) exit 0;;
esac
`
			journal := `#!/bin/sh
: > "$PREFLIGHT_FIXTURE_JOURNAL"
for arg in "$@"; do
 if [ "$arg" = "_SYSTEMD_INVOCATION_ID=$PREFLIGHT_FIXTURE_NEW" ]; then
  echo '[proxy_sandbox_unavailable] fixture-private-config'; exit 0
 fi
done
echo '[proxy_mark_unavailable] stale failure'; exit 0
`
			for name, body := range map[string]string{"systemctl": systemctl, "journalctl": journal} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			sd := &Systemd{UnitDir: dir}
			err := sd.CheckProxy(context.Background(), "/fixture/launcher", "public", nil)
			if err == nil || !strings.Contains(err.Error(), "["+tc.want+"]") {
				t.Fatalf("got %v want %s", err, tc.want)
			}
			_, called := os.Stat(filepath.Join(dir, "journal-called"))
			if (called == nil) != (tc.id == newID) {
				t.Fatal("historical or invalid invocation journal read")
			}
		})
	}
}
