package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"ctlvps/internal/domain"
)

func TestCoreVersionMigrationPreservesLegacyDefaultsAndExplicitPins(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
		missing, plain    bool
	}{
		{"missing", "", "1.12.14", true, false},
		{"encrypted empty", "", "1.12.14", false, false},
		{"whitespace", "  ", "1.12.14", false, false},
		{"plaintext empty", "", "1.12.14", false, true},
		{"explicit older", "1.13.21", "1.13.21", false, false},
		{"explicit new", "1.14.1", "1.14.1", false, false},
		{"plaintext pin", "1.12.14", "1.12.14", false, true},
		{"prerelease pin", "1.14.0-rc.1", "1.14.0-rc.1", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			// v30 changes data only, so this reconstructs a v29 installation,
			// including one with no users or servers yet.
			if _, err = s.db.Exec("UPDATE schema_version SET version=29; DELETE FROM settings WHERE key='core.singbox_version'"); err != nil {
				t.Fatal(err)
			}
			if !tc.missing {
				var value any = s.seal("settings.value", tc.value)
				if tc.plain {
					value = tc.value
				}
				if _, err = s.db.Exec("INSERT INTO settings(key,value) VALUES(?,?)", domain.SettingSingBoxVersion, value); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			for i := 0; i < 2; i++ {
				s, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := s.GetSetting(context.Background(), domain.SettingSingBoxVersion, "missing"); got != tc.want {
					t.Fatalf("open %d: got %q, want %q", i, got, tc.want)
				}
				var raw string
				if err = s.db.QueryRow("SELECT value FROM settings WHERE key=?", domain.SettingSingBoxVersion).Scan(&raw); err != nil || !strings.HasPrefix(raw, secretPrefix) {
					t.Fatal("pin not encrypted", err)
				}
				s.Close()
			}
		})
	}
}

func TestFreshCoreDefaultAndExplicitDefaultChoiceArePinned(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if got := s.GetSetting(ctx, domain.SettingSingBoxVersion, "missing"); got != "1.14.1" {
		t.Fatal(got)
	}
	if err := s.SetSetting(ctx, domain.SettingSingBoxVersion, "1.12.14"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, domain.SettingSiteName, "renamed"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSetting(ctx, domain.SettingSingBoxVersion, ""); got != "1.12.14" {
		t.Fatal("unrelated save changed core", got)
	}
	values := map[string]string{domain.SettingSingBoxVersion: ""}
	if err := s.SetSettings(ctx, values); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSetting(ctx, domain.SettingSingBoxVersion, ""); got != domain.DefaultSingBoxVersion {
		t.Fatal("default was not concretely pinned", got)
	}
	if values[domain.SettingSingBoxVersion] != "" {
		t.Fatal("mutated caller's draft")
	}
}

func TestCorePinMigrationFailsClosedAndRetriesAtomically(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.Exec("UPDATE schema_version SET version=29; UPDATE settings SET value='enc:v1:corrupt' WHERE key='core.singbox_version'"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err == nil {
		t.Fatal("unreadable pin accepted")
	}
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 29 {
		t.Fatal("failed migration advanced schema", version, err)
	}
	if _, err := s.db.Exec("UPDATE settings SET value='' WHERE key='core.singbox_version'"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if got := s.GetSetting(context.Background(), domain.SettingSingBoxVersion, ""); got != "1.12.14" {
		t.Fatal(got)
	}
}
