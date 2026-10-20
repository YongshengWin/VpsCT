package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/agentproto"
	"ctlvps/internal/networkconfig"
)

func recoveryFixture(t *testing.T) (string, *Systemd, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box")
	unitDir := filepath.Join(dir, "units")
	if err := os.Mkdir(unitDir, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "systemctl.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CORE_RECOVERY_TEST_LOG\"\ncase \"$1\" in\nis-active) echo active;;\nis-enabled) echo enabled;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CORE_RECOVERY_TEST_LOG", log)
	for path, content := range map[string]string{bin: "old executable fixture", bin + ".trusted": "old verified receipt", bin + ".activate": "old activation marker", bin + ".policy": "same-policy\n", filepath.Join(unitDir, singboxUnit): "old service unit"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return bin, &Systemd{UnitDir: unitDir}, log
}
func stageRecoveryFixture(t *testing.T, bin string) {
	t.Helper()
	r, err := prepareBinaryRecovery(bin)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{bin: "candidate executable fixture", bin + ".trusted": "candidate receipt", bin + ".activate": "candidate activation"} {
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	r.Phase = "installed"
	if err := writeCoreRecovery(bin, r); err != nil {
		t.Fatal(err)
	}
}
func requireRecoveryContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != want {
		t.Fatalf("recovery content mismatch for %s: %v", filepath.Base(path), err)
	}
}

func TestCoreActivationRestoresBinaryReceiptMarkerAndAllUnits(t *testing.T) {
	bin, sd, log := recoveryFixture(t)
	privateUnit := filepath.Join(sd.UnitDir, "ctlvps-singbox-private.service")
	if err := os.WriteFile(privateUnit, []byte("old private service"), 0644); err != nil {
		t.Fatal(err)
	}
	stageRecoveryFixture(t, bin)
	if err := beginCoreApply(context.Background(), bin, sd, "same-policy", []string{singboxUnit}, nil); err != nil {
		t.Fatal(err)
	}
	if err := markCoreApplying(bin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd.UnitDir, singboxUnit), []byte("candidate unit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateUnit, []byte("candidate private service"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := markCoreValidated(bin); err != nil {
		t.Fatal(err)
	}
	if err := finishCoreActivation(context.Background(), bin, sd, false); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{bin: "old executable fixture", bin + ".trusted": "old verified receipt", bin + ".activate": "old activation marker", bin + ".policy": "same-policy\n", filepath.Join(sd.UnitDir, singboxUnit): "old service unit"} {
		requireRecoveryContent(t, path, want)
	}
	requireRecoveryContent(t, privateUnit, "old private service")
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "stop ") || !strings.Contains(string(b), "start "+singboxUnit) {
		t.Fatal("previous active service was not restarted after restoring its executable")
	}
	if !strings.Contains(string(b), "start ctlvps-singbox-private.service") {
		t.Fatal("second permission group not restored")
	}
	if _, err := os.Stat(recoveryDir(bin)); !os.IsNotExist(err) {
		t.Fatal("finished rollback kept a pending journal")
	}
	if err := finishCoreActivation(context.Background(), bin, sd, false); err != nil {
		t.Fatal("rollback is not idempotent", err)
	}
}

func TestRetainedCoreIdentityRequiresAdmittedEndpointAndOldRunningBinary(t *testing.T) {
	bin, sd, _ := recoveryFixture(t)
	nodes := []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 21000, Params: map[string]any{"password": "retained-fixture", "method": "aes-128-gcm"}}}
	ds := &agentproto.DesiredState{}
	policy := corePolicyFingerprint(nodes, nil)
	intent := coreRetainedFingerprint(ds, nodes, nil)
	if err := os.WriteFile(bin+".policy", []byte(policy+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".retained", []byte(intent+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, ok := retainedCoreIdentity(context.Background(), bin, sd, ds, nodes, nil)
	if !ok {
		t.Fatal("admitted old service identity unavailable")
	}
	stageRecoveryFixture(t, bin)
	after, ok := retainedCoreIdentity(context.Background(), bin, sd, ds, nodes, nil)
	if !ok || before != after {
		t.Fatal("staged candidate replaced the retained running executable identity")
	}
	nodes[0].ListenPort++
	if _, ok := retainedCoreIdentity(context.Background(), bin, sd, ds, nodes, nil); ok {
		t.Fatal("unadmitted port retained")
	}
	nodes[0].ListenPort--
	nodes[0].Params["password"] = "rotated"
	if _, ok := retainedCoreIdentity(context.Background(), bin, sd, ds, nodes, nil); ok {
		t.Fatal("unadmitted credential retained")
	}
}

func TestRetainedCoreIgnoresSamplingButRejectsExpiredDNS(t *testing.T) {
	ds := &agentproto.DesiredState{}
	runtime := &networkconfig.Resolved{CollectorID: "fixture-collector", BootID: "fixture-boot", Sequence: 1, SOCKS5: &networkconfig.ResolvedSOCKS5{Host: "fixture.invalid", Address: "192.0.2.1", Port: 1080}}
	runtime.SOCKS5.SetDNSLifetime(time.Now().Add(-time.Second), 300)
	nodes := []agentproto.NodeSpec{{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 21000, Params: map[string]any{"password": "fixture"}, RuntimeNetwork: runtime}}
	before := coreRetainedFingerprint(ds, nodes, nil)
	runtime.Sequence++
	runtime.SOCKS5.SetDNSLifetime(time.Now(), 300)
	if before != coreRetainedFingerprint(ds, nodes, nil) {
		t.Fatal("sampling or renewed DNS lifetime invalidated a retained endpoint")
	}
	if !retainedRuntimeFresh(runtime) {
		t.Fatal("valid transport considered expired")
	}
	runtime.SOCKS5.ExpiresAt = time.Now().Add(-time.Second)
	if retainedRuntimeFresh(runtime) {
		t.Fatal("expired DNS transport retained")
	}
	runtime.BootID = "different-boot"
	if before == coreRetainedFingerprint(ds, nodes, nil) {
		t.Fatal("boot identity change retained")
	}
}

func TestCoreCompletedRecoveryCleanupSurvivesInterruptedRemoval(t *testing.T) {
	bin, sd, _ := recoveryFixture(t)
	done := recoveryDir(bin) + ".done"
	if err := os.Mkdir(done, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(done, "orphan.previous"), []byte("completed backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if pending, err := recoverCoreInstallation(context.Background(), bin, sd); err != nil || pending {
		t.Fatal("committed cleanup left a pending activation", err)
	}
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Fatal("committed backup not cleaned up")
	}
	requireRecoveryContent(t, bin, "old executable fixture")
}

func TestCoreRetirementSyncFailurePreservesCompleteRollback(t *testing.T) {
	bin, sd, _ := recoveryFixture(t)
	stageRecoveryFixture(t, bin)
	if err := beginCoreApply(context.Background(), bin, sd, "same-policy", []string{singboxUnit}, nil); err != nil {
		t.Fatal(err)
	}
	if err := markCoreApplying(bin); err != nil {
		t.Fatal(err)
	}
	if err := markCoreValidated(bin); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("fixture retirement fsync failure")
	syncs := 0
	err := removeCoreRecoveryWith(bin, os.Rename, os.RemoveAll, func(path string) error {
		syncs++
		if syncs == 1 {
			return wantErr
		}
		return syncDirectory(path)
	})
	if !errors.Is(err, wantErr) || syncs != 2 {
		t.Fatal("undurable journal retirement did not restore and sync the journal", err)
	}
	if _, err := os.Stat(recoveryDir(bin) + ".done"); !os.IsNotExist(err) {
		t.Fatal("failed retirement left the rollback journal hidden")
	}
	if err := finishCoreActivation(context.Background(), bin, sd, false); err != nil {
		t.Fatal("failed retirement cannot roll back", err)
	}
	requireRecoveryContent(t, bin, "old executable fixture")
	requireRecoveryContent(t, bin+".trusted", "old verified receipt")
	requireRecoveryContent(t, bin+".activate", "old activation marker")
}

func TestCoreDurableRetirementIgnoresCleanupFailures(t *testing.T) {
	for _, failure := range []string{"remove", "sync"} {
		t.Run(failure, func(t *testing.T) {
			bin, sd, _ := recoveryFixture(t)
			stageRecoveryFixture(t, bin)
			removes, syncs := 0, 0
			err := removeCoreRecoveryWith(bin, os.Rename, func(path string) error {
				removes++
				if failure == "remove" && removes == 2 {
					return errors.New("fixture completed backup cleanup failure")
				}
				return os.RemoveAll(path)
			}, func(path string) error {
				syncs++
				if failure == "sync" && syncs == 2 {
					return errors.New("fixture cleanup fsync failure")
				}
				return syncDirectory(path)
			})
			if err != nil || removes != 2 || syncs != 2 {
				t.Fatal("completed cleanup falsely reported a failed commit", err)
			}
			if r, err := loadCoreRecovery(bin); err != nil || r != nil {
				t.Fatal("durably retired recovery point remained active", err)
			}
			requireRecoveryContent(t, bin, "candidate executable fixture")
			if pending, err := recoverCoreInstallation(context.Background(), bin, sd); err != nil || pending {
				t.Fatal("cleanup retry changed the committed activation", err)
			}
			if _, err := os.Stat(recoveryDir(bin) + ".done"); !os.IsNotExist(err) {
				t.Fatal("completed cleanup was not retried")
			}
			requireRecoveryContent(t, bin, "candidate executable fixture")
		})
	}
}

func TestCoreUncertainRetirementCannotClaimRollback(t *testing.T) {
	bin, sd, _ := recoveryFixture(t)
	stageRecoveryFixture(t, bin)
	wantErr := errors.New("fixture failed to restore retirement")
	renames := 0
	err := removeCoreRecoveryWith(bin, func(from, to string) error {
		renames++
		if renames == 2 {
			return wantErr
		}
		return os.Rename(from, to)
	}, os.RemoveAll, func(string) error { return errors.New("fixture retirement fsync failure") })
	if !errors.Is(err, wantErr) {
		t.Fatal("failed retirement restoration was ignored", err)
	}
	if err := finishCoreActivation(context.Background(), bin, sd, false); err == nil {
		t.Fatal("hidden recovery evidence was reported as a completed rollback")
	}
	if _, err := os.Stat(filepath.Join(recoveryDir(bin)+".done", "journal.json")); err != nil {
		t.Fatal("uncertain retirement lost its recovery evidence", err)
	}
}

func TestCoreRollbackDoesNotRestartRevokedAuthorization(t *testing.T) {
	bin, sd, log := recoveryFixture(t)
	stageRecoveryFixture(t, bin)
	guard := filepath.Join(filepath.Dir(bin), "authorization.guard")
	if err := os.WriteFile(guard, []byte("revoked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := beginCoreApply(context.Background(), bin, sd, "revoked-policy", []string{singboxUnit}, nil); err != nil {
		t.Fatal(err)
	}
	if err := markCoreApplying(bin); err != nil {
		t.Fatal(err)
	}
	if err := finishCoreActivation(context.Background(), bin, sd, false); err != nil {
		t.Fatal(err)
	}
	requireRecoveryContent(t, bin, "old executable fixture")
	requireRecoveryContent(t, guard, "revoked")
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "start ") || strings.Contains(string(b), "enable ") || !strings.Contains(string(b), "disable "+singboxUnit) {
		t.Fatal("rollback resurrected previous authorization")
	}
}

func TestCoreSnapshotFailureFencesUnitsOutsidePartialJournal(t *testing.T) {
	bin, sd, log := recoveryFixture(t)
	stageRecoveryFixture(t, bin)
	// The installer has retained the binary but unit snapshots could not start.
	// A changed authorization must fence existing units even without that list.
	cause := errors.New("fixture snapshot failure")
	if err := rollbackCoreAfterFailure(bin, sd, "revoked-policy", cause); !errors.Is(err, cause) {
		t.Fatal("original activation error lost", err)
	}
	requireRecoveryContent(t, bin, "old executable fixture")
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "stop "+singboxUnit) || !strings.Contains(string(b), "disable "+singboxUnit) || strings.Contains(string(b), "start ") {
		t.Fatal("unrecorded unit retained revoked authorization")
	}
}

func TestCoreInstallationAndApplyRecoverAfterInterruption(t *testing.T) {
	for _, phase := range []string{"installing", "preflight", "applying", "validated"} {
		t.Run(phase, func(t *testing.T) {
			bin, sd, _ := recoveryFixture(t)
			stageRecoveryFixture(t, bin)
			if phase != "installing" {
				if err := beginCoreApply(context.Background(), bin, sd, "same-policy", []string{singboxUnit}, nil); err != nil {
					t.Fatal(err)
				}
			}
			r, err := loadCoreRecovery(bin)
			if err != nil {
				t.Fatal(err)
			}
			r.Phase = phase
			if err = writeCoreRecovery(bin, r); err != nil {
				t.Fatal(err)
			}
			if phase == "installing" {
				if _, err = recoverCoreInstallation(context.Background(), bin, sd); err != nil {
					t.Fatal(err)
				}
			} else {
				if pending, err := recoverCoreInstallation(context.Background(), bin, sd); err != nil || !pending {
					t.Fatal("unfinished activation skipped", err)
				}
				if err = beginCoreApply(context.Background(), bin, sd, "same-policy", []string{singboxUnit}, nil); err == nil || !strings.Contains(err.Error(), "interrupted") {
					t.Fatal("interrupted apply did not report recovery", err)
				}
			}
			requireRecoveryContent(t, bin, "old executable fixture")
			requireRecoveryContent(t, bin+".trusted", "old verified receipt")
			if _, err = os.Stat(recoveryDir(bin)); !os.IsNotExist(err) {
				t.Fatal("recovered journal retained")
			}
		})
	}
}

func TestCoreActivationCommitWaitsForAdmission(t *testing.T) {
	bin, sd, _ := recoveryFixture(t)
	stageRecoveryFixture(t, bin)
	if err := beginCoreApply(context.Background(), bin, sd, "same-policy", []string{singboxUnit}, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishCoreActivation(context.Background(), bin, sd, true); err == nil {
		t.Fatal("unvalidated core committed")
	}
	if err := markCoreValidated(bin); err != nil {
		t.Fatal(err)
	}
	if !activationPending(bin) {
		t.Fatal("marker removed before network admission")
	}
	if _, err := os.Stat(recoveryDir(bin)); err != nil {
		t.Fatal("old binary discarded before admission", err)
	}
	if err := finishCoreActivation(context.Background(), bin, sd, true); err != nil {
		t.Fatal(err)
	}
	requireRecoveryContent(t, bin, "candidate executable fixture")
	if activationPending(bin) {
		t.Fatal("committed marker retained")
	}
	if _, err := os.Stat(recoveryDir(bin)); !os.IsNotExist(err) {
		t.Fatal("committed recovery retained")
	}
}

func TestCoreRecoveryRefusesTamperedBackupAndUnauthorizedPath(t *testing.T) {
	for _, bad := range []string{"checksum", "target"} {
		t.Run(bad, func(t *testing.T) {
			bin, sd, _ := recoveryFixture(t)
			stageRecoveryFixture(t, bin)
			r, err := loadCoreRecovery(bin)
			if err != nil {
				t.Fatal(err)
			}
			if bad == "checksum" {
				err = os.WriteFile(filepath.Join(recoveryDir(bin), r.Files[0].Backup), []byte("tampered"), 0600)
			} else {
				r.Files[0].Path = filepath.Join(filepath.Dir(bin), "authorization.guard")
				err = writeCoreRecovery(bin, r)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = finishCoreActivation(context.Background(), bin, sd, false); err == nil {
				t.Fatal("unsafe restore accepted")
			}
			requireRecoveryContent(t, bin, "candidate executable fixture")
			if _, err = os.Stat(recoveryDir(bin)); err != nil {
				t.Fatal("unsafe recovery evidence deleted", err)
			}
		})
	}
}

func TestCoreRecoveryFilePreservesConfigurationPermissions(t *testing.T) {
	bin, _, _ := recoveryFixture(t)
	path := filepath.Join(filepath.Dir(bin), "private-config.json")
	if err := os.WriteFile(path, []byte("old private configuration"), 0640); err != nil {
		t.Fatal(err)
	}
	r, err := newCoreRecovery(bin, "installed")
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshotCoreFile(bin, r, path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("candidate configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = restoreCoreFile(bin, r.Files[0]); err != nil {
		t.Fatal(err)
	}
	requireRecoveryContent(t, path, "old private configuration")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0640 {
		t.Fatal("configuration permission changed", err)
	}
}

func TestCorePolicyFingerprintTracksCredentialsAndLocalRevocation(t *testing.T) {
	n := agentproto.NodeSpec{NodeID: 1, Core: "singbox", Protocol: "ss", ListenPort: 21000, Params: map[string]any{"password": "first", "method": "aes-128-gcm"}}
	before := corePolicyFingerprint([]agentproto.NodeSpec{n}, nil)
	n.Params["method"] = "invalid-fixture-cipher"
	n.ListenPort = 21001
	if corePolicyFingerprint([]agentproto.NodeSpec{n}, nil) != before {
		t.Fatal("ordinary activation change unnecessarily revokes rollback")
	}
	n.Params["password"] = "rotated"
	if corePolicyFingerprint([]agentproto.NodeSpec{n}, nil) == before {
		t.Fatal("credential rotation not fenced")
	}
	n.Params["password"] = "first"
	n.AllowPrivate = true
	if corePolicyFingerprint([]agentproto.NodeSpec{n}, nil) == before {
		t.Fatal("local private grant not fenced")
	}
	n.AllowPrivate = false
	n.Blocked = true
	if corePolicyFingerprint([]agentproto.NodeSpec{n}, nil) == before {
		t.Fatal("quota block not fenced")
	}
}
