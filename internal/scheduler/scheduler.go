// Package scheduler runs periodic maintenance jobs.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Job is a named periodic task.
type Job struct {
	Name     string
	Interval time.Duration
	RunAtStart bool
	Fn       func(ctx context.Context) error
}

// Scheduler runs jobs until the context is cancelled.
type Scheduler struct {
	Logger *slog.Logger
	jobs   []Job
	mu     sync.Mutex
	last   map[string]time.Time
	errs   map[string]string
}

// New builds a scheduler.
func New(logger *slog.Logger) *Scheduler {
	return &Scheduler{Logger: logger, last: map[string]time.Time{}, errs: map[string]string{}}
}

// Add registers a job.
func (s *Scheduler) Add(j Job) { s.jobs = append(s.jobs, j) }

// Run blocks until ctx is done.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, j := range s.jobs {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			if j.RunAtStart {
				s.exec(ctx, j)
			}
			t := time.NewTicker(j.Interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.exec(ctx, j)
				}
			}
		}(j)
	}
	wg.Wait()
}

func (s *Scheduler) exec(ctx context.Context, j Job) {
	defer func() {
		if r := recover(); r != nil {
			s.Logger.Error("job panicked", "job", j.Name, "panic", r)
		}
	}()
	start := time.Now()
	err := j.Fn(ctx)
	s.mu.Lock()
	s.last[j.Name] = start
	if err != nil {
		s.errs[j.Name] = err.Error()
	} else {
		delete(s.errs, j.Name)
	}
	s.mu.Unlock()
	if err != nil {
		s.Logger.Warn("job failed", "job", j.Name, "err", err, "took", time.Since(start))
	} else {
		s.Logger.Debug("job ok", "job", j.Name, "took", time.Since(start))
	}
}

// Status reports last run times and errors.
func (s *Scheduler) Status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	for _, j := range s.jobs {
		entry := map[string]any{"interval_sec": int(j.Interval.Seconds())}
		if t, ok := s.last[j.Name]; ok {
			entry["last_run"] = t
		}
		if e, ok := s.errs[j.Name]; ok {
			entry["error"] = e
		}
		out[j.Name] = entry
	}
	return out
}
