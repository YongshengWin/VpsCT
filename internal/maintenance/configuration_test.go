package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigurationWorkerWaitsWithinItsDeadline(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	release, err := m.ConfigurationLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := m.WaitConfigurationLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("worker bypassed coordinator ownership or ignored deadline", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		unlock, err := m.WaitConfigurationLock(ctx)
		if err == nil {
			defer unlock()
		}
		result <- err
	}()
	release()
	if err := <-result; err != nil {
		t.Fatal("worker did not take over after coordinator release", err)
	}
	// A cancelled request must not take an otherwise free target.
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := m.WaitConfigurationLock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled worker acquired target", err)
	}
}

func TestConfigurationAndMaintenanceShareDurableAgentTarget(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(exe, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Dir: t.TempDir(), Executable: exe, Launch: func(string, string) error { return nil }}
	release, err := m.ConfigurationLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other := &Manager{Dir: m.Dir, Executable: exe, Launch: m.Launch}
	if _, err := other.ConfigurationLock(); !errors.Is(err, ErrBusy) {
		t.Fatal("second coordinator overlapped configuration", err)
	}
	spec := Spec{Request: Request{ID: NewID(), Role: "agent", Action: "uninstall"}}
	if _, err := other.Start(spec); !errors.Is(err, ErrBusy) {
		t.Fatal("maintenance overlapped an active configuration", err)
	}
	if _, err := m.Get(spec.ID); !os.IsNotExist(err) {
		t.Fatal("rejected maintenance reserved a job", err)
	}
	release()
	release() // idempotent release must not unlock a later caller's descriptor
	job, err := other.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConfigurationLock(); !errors.Is(err, ErrBusy) {
		t.Fatal("restart ignored durable maintenance reservation", err)
	}
	// An admitted worker retains the actual lock during execution, even if a
	// stale recovery record says it ended. Status alone cannot unlock a process.
	workerRelease, err := other.tryConfigurationLock()
	if err != nil {
		t.Fatal(err)
	}
	defer workerRelease()
	job.Status, job.UpdatedAt = "interrupted", time.Now().UTC()
	if err := writeJSON(m.path(job.ID, "status.json"), job); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConfigurationLock(); !errors.Is(err, ErrBusy) {
		t.Fatal("stale status bypassed worker ownership", err)
	}
	workerRelease()
	release, err = m.ConfigurationLock()
	if err != nil {
		t.Fatal("finished maintenance kept target locked", err)
	}
	release()
}

func TestControllerMaintenanceDoesNotReserveAgentConfiguration(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	j := Job{Request: Request{ID: NewID(), Role: "controller", Action: "uninstall"}, Status: "running", UpdatedAt: time.Now().UTC()}
	if err := os.Mkdir(m.path(j.ID, ""), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(m.path(j.ID, "status.json"), j); err != nil {
		t.Fatal(err)
	}
	release, err := m.ConfigurationLock()
	if err != nil {
		t.Fatal("controller-only maintenance unnecessarily reserved the agent", err)
	}
	release()
}
