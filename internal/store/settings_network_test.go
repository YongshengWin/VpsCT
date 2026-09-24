package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ctlvps/internal/domain"
)

func TestCorePinAndNetworkBindingCheckEachOther(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, profile, n := egressFixture(t, s)
	if err := s.SetSetting(ctx, domain.SettingSingBoxVersion, "1.15.0"); err != nil {
		t.Fatal("unbound legacy configuration was restricted", err)
	}
	if _, err := s.SetNodeNetwork(ctx, n.ID, 0, nodePolicy(profile), nil); !errors.Is(err, ErrNetworkCoreVersion) {
		t.Fatal("unsupported core accepted a binding", err)
	}
	if err := s.SetSetting(ctx, domain.SettingSingBoxVersion, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNodeNetwork(ctx, n.ID, 0, nodePolicy(profile), nil); err != nil {
		t.Fatal("default supported pin rejected", err)
	}
	// Paused resources retain their network constraint when resumed.
	if _, err := s.db.Exec(`UPDATE nodes SET enabled=0 WHERE id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: "1.15.0", domain.SettingSiteName: "must not be saved"}); !errors.Is(err, ErrNetworkCoreVersion) {
		t.Fatal("changed pin on a paused binding", err)
	}
	if got := s.GetSetting(ctx, domain.SettingSiteName, "unchanged"); got != "unchanged" {
		t.Fatal("rejected form partially saved")
	}
	if err := s.SetSetting(ctx, domain.SettingSingBoxVersion, "v"+domain.DefaultSingBoxVersion); err != nil {
		t.Fatal("equivalent supported pin rejected", err)
	}
	if _, err := s.SetNodeNetwork(ctx, n.ID, 1, nil, nil); err != nil {
		t.Fatal("cannot explicitly detach", err)
	}
	if err := s.SetSetting(ctx, domain.SettingSingBoxVersion, "1.15.0"); err != nil {
		t.Fatal("cleared policy treated as active", err)
	}
}

func TestOfficialPatchUpgradeRetainsNetworkBinding(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, profile, n := egressFixture(t, s)
	if _, err := s.SetNodeNetwork(ctx, n.ID, 0, nodePolicy(profile), nil); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1.13.21", "1.14.1", "1.14.2"} {
		if err := s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: version}); err != nil {
			t.Fatal(version, err)
		}
		got, err := s.GetNode(ctx, n.ID)
		if err != nil || got.Network == nil || got.NetworkRevision != 1 {
			t.Fatal("upgrade changed binding", got.Network, err)
		}
	}
	if err := s.SetSettings(ctx, map[string]string{domain.SettingSingBoxVersion: "1.14.1-ctlvps.1"}); !errors.Is(err, ErrNetworkCoreVersion) {
		t.Fatal("custom build accepted", err)
	}
}

func TestSettingsBatchRollsBackOnWriteFailure(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_setting_failure BEFORE INSERT ON settings WHEN NEW.key='zz_failure' BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(ctx, map[string]string{domain.SettingSiteName: "partial", "zz_failure": "x"}); err == nil {
		t.Fatal("expected fixture write failure")
	}
	if got := s.GetSetting(ctx, domain.SettingSiteName, "unchanged"); got != "unchanged" {
		t.Fatal("first setting survived failed batch")
	}
}

func TestBindingRejectsUnsupportedOrMismatchedCore(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		core     domain.Core
	}{{"snell", domain.CoreSnell}, {"snell", domain.CoreSingBox}, {"unknown", domain.CoreSingBox}} {
		t.Run(tc.protocol+"/"+string(tc.core), func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			_, profile, n := egressFixture(t, s)
			if _, err := s.db.Exec(`UPDATE nodes SET protocol=?,core=? WHERE id=?`, tc.protocol, tc.core, n.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetNodeNetwork(ctx, n.ID, 0, nodePolicy(profile), nil); !errors.Is(err, ErrNetworkCoreVersion) {
				t.Fatal("unsupported combination saved a binding", err)
			}
		})
	}
}

func TestConcurrentCorePinAndBindingCannotCommitIncompatiblePair(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			_, profile, n := egressFixture(t, s)
			start, result := make(chan struct{}), make(chan error, 2)
			go func() { <-start; result <- s.SetSetting(ctx, domain.SettingSingBoxVersion, "1.15.0") }()
			go func() { <-start; _, err := s.SetNodeNetwork(ctx, n.ID, 0, nodePolicy(profile), nil); result <- err }()
			close(start)
			a, b := <-result, <-result
			if a == nil && b == nil {
				t.Fatal("both incompatible writes committed")
			}
			if a != nil && b != nil {
				t.Fatal("neither concurrent write made progress", a, b)
			}
			got, err := s.GetNode(ctx, n.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Network != nil && !bindingCoreVersionSupported(s.GetSetting(ctx, domain.SettingSingBoxVersion, "")) {
				t.Fatal("incompatible final state")
			}
		})
	}
}
