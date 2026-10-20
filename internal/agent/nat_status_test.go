package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/natconfig"
	"ctlvps/internal/nft"
)

func TestNATAuthorityNeverOverwritesLocalOptOutOrBadFile(t *testing.T) {
	remote := &natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 10001}}}
	for _, tc := range []struct {
		name             string
		local            natconfig.Config
		present          bool
		localErr         error
		remote           *natconfig.Config
		source           string
		configured, fail bool
	}{
		{"none", natconfig.Config{Schema: 1}, false, nil, nil, "none", false, false},
		{"controller", natconfig.Config{Schema: 1}, false, nil, remote, "controller", true, false},
		{"controller disabled", natconfig.Config{Schema: 1}, false, nil, &natconfig.Config{Schema: 1}, "controller", false, false},
		{"local disabled", natconfig.Config{Schema: 1}, true, nil, remote, "local", false, false},
		{"local enabled", *remote, true, nil, &natconfig.Config{Schema: 1}, "local", true, false},
		{"unsafe local", natconfig.Config{}, true, errors.New("unsafe local owner"), remote, "local", false, true},
		{"malformed local", natconfig.Config{Schema: 7}, true, nil, remote, "local", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := selectNATAuthority(tc.local, tc.present, tc.localErr, tc.remote)
			if s.Source != tc.source || s.Configured != tc.configured || (err != nil) != tc.fail || s.Status == "applied" {
				t.Fatalf("selected=%+v err=%v", s, err)
			}
			if tc.fail && (s.Status != "error" || s.Config != nil) {
				t.Fatal("bad authority fell back or exposed an untrusted snapshot:", s)
			}
			if err := agentproto.ValidateNAT(s); err != nil {
				t.Fatal(err)
			}
		})
	}
	s, err := selectNATAuthority(natconfig.Config{Schema: 1}, false, nil, remote)
	if err != nil {
		t.Fatal(err)
	}
	s.Config.Mappings[0].PublicPort = 40000
	if remote.Mappings[0].PublicPort != 30001 {
		t.Fatal("reported snapshot aliases controller configuration")
	}
}

func natFixtureAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	bin, log := filepath.Join(dir, "nft"), filepath.Join(dir, "calls")
	// Pretend our table exists so cleanup must be checked and committed rather
	// than returning through the absent-table no-op. No real nft command runs.
	script := "#!/bin/sh\nbody=$(cat)\nprintf '%s\\n' \"$body\" >> '" + log + "'\nprintf '%s' '{\"nftables\":[{\"table\":{\"family\":\"inet\",\"name\":\"ctlvps_nat\"}}]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ds := &agentproto.DesiredState{Revision: 4, Hash: strings.Repeat("a", 64)}
	a := &Agent{NFT: &nft.Manager{Bin: bin}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StateDir: dir,
		State: &State{ServerURL: "https://fixture.example", AgentToken: "fixture", AppliedRevision: 4, AppliedHash: ds.Hash, NodeApplicationRevision: 4, NodeApplicationHash: ds.Hash, NodeApplicationSequence: 8, NodeApplications: []agentproto.NodeApplication{{NodeID: 1, Status: "applied"}}}, desired: ds}
	return a, log
}

func TestNATControllerEnableAndExplicitDisableRequireV2(t *testing.T) {
	a, log := natFixtureAgent(t)
	enabled := natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 30001}}}
	for _, cfg := range []natconfig.Config{enabled, {Schema: 1}} {
		ds := &agentproto.DesiredState{NATVersion: agentproto.NATVersion, NAT: &cfg}
		if err := a.rememberNATIntent(ds); err != nil {
			t.Fatal(err)
		}
		choice, err := selectNATAuthority(natconfig.Config{Schema: 1}, false, nil, a.natController)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.applyNATChoice(context.Background(), choice, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		reported := a.natDiagnosticsChoice(choice, nil, nil)
		want := "applied"
		if len(cfg.Mappings) == 0 {
			want = "disabled"
		}
		if reported.Source != "controller" || reported.Status != want || a.State.NATVersion != 2 {
			t.Fatalf("report=%+v state=%+v", reported, a.State)
		}
		st, err := LoadState(a.StateDir)
		if err != nil || st.NATVersion != 2 {
			t.Fatalf("NAT v2 not durable: %v %v", st, err)
		}
	}
	body, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(body), "delete table inet ctlvps_nat") {
		t.Fatal("explicit disable did not remove the owned table")
	}
	if err := a.rememberNATIntent(&agentproto.DesiredState{}); err != nil {
		t.Fatal(err)
	}
	if a.State.NATVersion != 2 || a.natController != nil {
		t.Fatal("removing controller config erased the sticky requirement")
	}
	// An explicit empty controller config still needs v2 on its first use.
	fresh, _ := natFixtureAgent(t)
	if err := fresh.rememberNATIntent(&agentproto.DesiredState{NATVersion: 2, NAT: &natconfig.Config{Schema: 1}}); err != nil || fresh.State.NATVersion != 2 {
		t.Fatal("fresh remote opt-out did not establish v2:", err)
	}
}

func TestNATReadOnlyReportCannotInheritStaleOrPartialSuccess(t *testing.T) {
	a, log := natFixtureAgent(t)
	cfg := natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 30001}}}
	choice := natChoice(cfg, "local", nil)
	if err := a.applyNATChoice(context.Background(), choice, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(log)
	if a.State.NATVersion != 1 {
		t.Fatal("old local configuration should retain v1 compatibility")
	}
	if s := a.natDiagnosticsChoice(natChoice(cfg, "local", nil), nil, nil); s.Status != "applied" {
		t.Fatal(s)
	}
	denied := errors.New("configuration not authorized")
	if s := a.natDiagnosticsChoice(natChoice(cfg, "local", nil), nil, denied); s.Status != "pending" || s.Config == nil {
		t.Fatal(s)
	}
	changed := cfg.Normalized()
	changed.Mappings[0].PublicPort, changed.Mappings[0].LocalPort = 30002, 30002
	if s := a.natDiagnosticsChoice(natChoice(changed, "local", nil), nil, nil); s.Status != "pending" {
		t.Fatal("changed same-revision local config inherited success:", s)
	}
	a.State.ApplyError = "failed sibling"
	if s := a.natDiagnosticsChoice(natChoice(cfg, "local", nil), nil, nil); s.Status != "pending" {
		t.Fatal("partial verification became complete:", s)
	}
	a.State.ApplyError = ""
	a.desired = nil
	if s := a.natDiagnosticsChoice(natChoice(cfg, "local", nil), nil, nil); s.Status != "pending" {
		t.Fatal("missing desired context became applied:", s)
	}
	bad := errors.New("unreadable local file")
	if s := a.natDiagnosticsChoice(natChoice(natconfig.Config{}, "local", bad), bad, denied); s.Status != "error" || s.Config != nil {
		t.Fatal(s)
	}
	after, _ := os.ReadFile(log)
	if string(after) != string(before) {
		t.Fatal("reporting performed a firewall mutation")
	}
}

func TestNATSameRevisionLocalFailureThenVerifiedRecovery(t *testing.T) {
	a, _ := natFixtureAgent(t)
	bad := errors.New("invalid local mapping")
	a.refreshNATForDesired(context.Background(), a.desired, natconfig.Config{}, bad)
	if a.natStatus.Status != "error" || a.natReady || a.State.NodeApplicationSequence != 9 || a.State.NodeApplications[0].Status != "failed" {
		t.Fatalf("failed authority retained success: %+v %+v", a.natStatus, a.State)
	}
	// The ordinary full retry owns node receipts. Until it confirms the same
	// revision again, valid NAT alone must not advertise a completed application.
	good := natconfig.Config{Schema: 1, Mappings: []natconfig.Mapping{{PublicPort: 30001, LocalPort: 30001}}}
	if err := a.syncNATConfig(context.Background(), good, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := a.natDiagnosticsChoice(natChoice(good, "local", nil), nil, nil); s.Status != "pending" {
		t.Fatal("NAT-only retry advertised full success:", s)
	}
	a.State.ApplyError = ""
	if s := a.natDiagnosticsChoice(natChoice(good, "local", nil), nil, nil); s.Status != "applied" {
		t.Fatal(s)
	}
	if a.State.AppliedRevision != 4 {
		t.Fatal("local change invented a remote revision")
	}
}

func TestNATErrorTextBoundAndPersistedRequirements(t *testing.T) {
	err := errors.New(strings.Repeat("错", 200) + string([]byte{0xff}))
	text := boundedNATError(err)
	if len(text) > 512 || !utf8.ValidString(text) || text == "" {
		t.Fatal("invalid bounded text:", len(text))
	}
	for _, version := range []int{0, 1, 2, 3, -1} {
		a, _ := natFixtureAgent(t)
		a.State.NATVersion = version
		if err := a.State.Save(a.StateDir); err != nil {
			t.Fatal(err)
		}
		_, err := LoadState(a.StateDir)
		if (err == nil) != (version >= 0 && version <= 2) {
			t.Fatalf("persisted NAT version %d: %v", version, err)
		}
	}
}
