// Package agentwork owns the single bounded heavy-work slot. It never queues
// tasks: the coordinator retries the latest desired state on its next tick.
package agentwork

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"ctlvps/internal/agentbudget"
)

var ErrPending = errors.New("resource worker pending")
var slot struct {
	sync.Mutex
	job       *job
	failedKey [32]byte
	failedAt  time.Time
	failed    error
}

type job struct {
	key      [32]byte
	done     chan error
	finished bool
	result   error
}
type output struct{ data []byte }

func (w *output) Write(p []byte) (int, error) {
	n := len(p)
	room := 4096 - len(w.data)
	w.data = append(w.data, p[:min(n, room)]...)
	return n, nil
}

func Available() bool { return runtime.GOOS == "linux" && os.Geteuid() == 0 }
func Pending() bool {
	slot.Lock()
	defer slot.Unlock()
	refreshLocked()
	return slot.job != nil && !slot.job.finished
}

// Start returns immediately. All descendants inherit the agent's systemd
// cgroup, including its aggregate MemoryMax; no work escapes the service budget.
func Start(ctx context.Context, operation string, input []byte) error {
	if operation != "core-install" && operation != "agent-update" {
		return errors.New("unknown resource operation")
	}
	if len(input) > 64<<10 {
		return errors.New("resource request exceeds budget")
	}
	key := sha256.Sum256(append([]byte(operation), input...))
	slot.Lock()
	defer slot.Unlock()
	refreshLocked()
	if slot.job != nil {
		if !slot.job.finished {
			return ErrPending
		}
		previous, err := slot.job.key, slot.job.result
		slot.job = nil
		if err != nil {
			slot.failedKey = previous
			slot.failedAt = time.Now()
			slot.failed = err
		}
		if previous == key {
			return err
		}
	}
	if slot.failedKey == key && slot.failed != nil && time.Since(slot.failedAt) < 30*time.Second {
		return slot.failed
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	workCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	cmd := exec.CommandContext(workCtx, exe, operation)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "GOMAXPROCS=2", "GOMEMLIMIT=" + agentbudget.WorkerGoLimit}
	cmd.Stdin = strings.NewReader(string(input))
	cmd.WaitDelay = time.Second
	var stderr output
	cmd.Stderr = &stderr
	bindLifetime(cmd)
	if err = cmd.Start(); err != nil {
		cancel()
		return err
	}
	j := &job{key: key, done: make(chan error, 1)}
	slot.job = j
	go func() {
		err := cmd.Wait()
		cancel()
		if err != nil {
			err = fmt.Errorf("resource worker: %w: %s", err, strings.TrimSpace(string(stderr.data)))
		}
		j.done <- err
	}()
	return ErrPending
}

func refreshLocked() {
	if slot.job == nil || slot.job.finished {
		return
	}
	select {
	case err := <-slot.job.done:
		slot.job.result = err
		slot.job.finished = true
	default:
	}
}
