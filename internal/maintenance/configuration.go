package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Admission callers use tryConfigurationLock under admission.lock, always in
// that order. An already admitted worker also holds this file during execution
// so a stale status or coordinator restart cannot permit concurrent configure.
func (m *Manager) tryConfigurationLock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(m.Dir, "agent-configuration.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }) }, nil
}

// ConfigurationLock reserves the local agent target until release. Starting a
// maintenance job checks this same lock before persisting its active state;
// subsequent configuration sees that durable state even after daemon restart.
// It does not lock the controller's configuration or wait behind a long job.
func (m *Manager) ConfigurationLock() (func(), error) {
	if err := os.MkdirAll(m.Dir, 0700); err != nil {
		return nil, err
	}
	admission, err := os.OpenFile(filepath.Join(m.Dir, "admission.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer admission.Close()
	if err = unix.Flock(int(admission.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	defer unix.Flock(int(admission.Fd()), unix.LOCK_UN)
	if action, err := m.ActiveAction("agent"); err != nil {
		return nil, err
	} else if action != "" {
		return nil, ErrBusy
	}
	return m.tryConfigurationLock()
}

// WaitConfigurationLock is for a bounded background worker. Its coordinator
// may still own the target while launching it; the worker must acquire its own
// lock after that coordinator returns, and retain it through the mutation.
// Every retry checks durable maintenance admission again.
func (m *Manager) WaitConfigurationLock(ctx context.Context) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := m.ConfigurationLock()
		if err == nil {
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			return release, nil
		}
		if !errors.Is(err, ErrBusy) {
			return nil, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
