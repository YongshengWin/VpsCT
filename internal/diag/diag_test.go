package diag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureChronyUsesOpenRCServiceAndRunlevel(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("PATH", dir)
	t.Setenv("DIAG_FIXTURE_LOG", log)
	for _, bin := range []string{"rc-service", "rc-update", "systemctl"} {
		body := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$DIAG_FIXTURE_LOG\"\n"
		if bin == "rc-service" {
			body += "if [ \"$1\" = --exists ]; then [ \"$2\" = chronyd ]; else exit 0; fi\n"
		} else if bin == "systemctl" {
			body += "exit 1\n"
		}
		if err := os.WriteFile(filepath.Join(dir, bin), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ensureChrony(context.Background(), true)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "rc-service --exists chronyd\nrc-update add chronyd default\nrc-service chronyd start\n"
	if string(raw) != want {
		t.Fatalf("unexpected service manager commands: %s", raw)
	}
}

func TestEnsureChronyDoesNotInventMissingOpenRCService(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("PATH", dir)
	t.Setenv("DIAG_FIXTURE_LOG", log)
	for _, bin := range []string{"rc-service", "rc-update", "systemctl"} {
		body := "#!/bin/sh\nprintf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$DIAG_FIXTURE_LOG\"\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, bin), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ensureChrony(context.Background(), true)
	raw, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(raw), "rc-update") || strings.Contains(string(raw), "systemctl") || strings.Contains(string(raw), " start") {
		t.Fatalf("invented a time service: %s %v", raw, err)
	}
}
