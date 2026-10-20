//go:build linux

package maintenance

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ctlvps/internal/openrc"
)

func requireOpenRCFixture(t *testing.T) {
	t.Helper()
	if os.Getenv("CTLVPS_ALPINE_FIXTURE") != "1" || !openrc.Enabled() {
		t.Skip("isolated Alpine fixture required")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("disposable container required")
	}
}

func TestKernelOpenRCMaintenanceUpdateAndRollback(t *testing.T) {
	requireOpenRCFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	candidate, err := os.ReadFile("/fixtures/ctlvps-agent")
	if err != nil {
		t.Fatal(err)
	}
	oldDownload, oldVerify := downloadAgentTo, verifyAgentRelease
	defer func() { downloadAgentTo = oldDownload; verifyAgentRelease = oldVerify }()
	// Exercise lifecycle separately from publisher verification, which has its
	// own trust tests. The fixture has no network and uses locally built agents.
	verifyAgentRelease = func(context.Context, string, string, io.ReadSeeker) error { return nil }
	m := NewManager()
	for _, fail := range []bool{false, true} {
		payload := candidate
		if fail {
			payload = []byte("#!/bin/sh\nif [ \"$1\" = run ]; then exit 7; fi\nexec /fixtures/ctlvps-agent \"$@\"\n")
		}
		downloadAgentTo = func(_ context.Context, _ string, dst io.Writer) (int64, error) {
			n, e := dst.Write(payload)
			return int64(n), e
		}
		id := NewID()
		if err := os.MkdirAll(m.path(id, ""), 0700); err != nil {
			t.Fatal(err)
		}
		path := m.path(id, "payload")
		if err := os.WriteFile(path, payload, 0700); err != nil {
			t.Fatal(err)
		}
		sum, err := hashFile(path)
		if err != nil {
			t.Fatal(err)
		}
		release, err := m.tryConfigurationLock()
		if err != nil {
			t.Fatal(err)
		}
		var log bytes.Buffer
		status, _, err := m.updateAgent(ctx, Spec{Request: Request{ID: id, Role: "agent", Action: "update", Version: "v9.9.9"}, DownloadURL: "https://controller.invalid/fixture", SHA256: sum}, &log, func(string, string, string) {}, release)
		release()
		want := "succeeded"
		if fail {
			want = "rolled_back"
		}
		if status != want {
			t.Fatalf("fail=%v status=%s err=%v log=%s", fail, status, err, log.String())
		}
		if err := openrc.AgentReady(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKernelOpenRCMaintenanceUninstall(t *testing.T) {
	requireOpenRCFixture(t)
	m := NewManager()
	m.Executable = "/fixtures/ctlvps-agent"
	id := NewID()
	purge := os.Getenv("CTLVPS_TEST_PURGE") == "1"
	s := Spec{Request: Request{ID: id, Role: "agent", Action: "uninstall", Purge: purge}}
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	if !openrc.MaintenanceActive(id) {
		t.Fatal("worker did not leave the agent cgroup before dispatch returned")
	}
	// Recovery must preserve a genuinely running worker, including when old.
	j, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	j.UpdatedAt = time.Now().Add(-2 * time.Minute)
	if err := writeJSON(m.path(id, "status.json"), j); err != nil {
		t.Fatal(err)
	}
	m.Recover()
	if j, err := m.Get(id); err != nil || !j.Active() {
		t.Fatal("live worker recovered as interrupted", j, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		j, err = m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !j.Active() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if j.Status != "succeeded" {
		log, _ := os.ReadFile(m.path(id, "worker.log"))
		t.Fatalf("uninstall: %+v log=%s", j, log)
	}
	if _, err := os.Stat("/usr/local/bin/ctlvps-agent"); !os.IsNotExist(err) {
		t.Fatal("agent executable retained", err)
	}
	_, err = os.Stat("/var/lib/ctlvps-agent/state.json")
	if purge && !os.IsNotExist(err) || !purge && err != nil {
		t.Fatal("incorrect data retention", err)
	}
	// Durable idempotency survives both executable and enrollment deletion.
	if old, err := m.Start(s); err != nil || old.Status != "succeeded" {
		t.Fatal(old, err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, id, "status.json")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "test ! -e /etc/init.d/ctlvps-agent")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(strings.TrimSpace(string(out)), err)
	}
}
