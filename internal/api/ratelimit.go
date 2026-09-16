package api

import (
	"sync"
	"time"
)

// rateLimiter is a small fixed-window limiter keyed by string (ip).
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newRateLimiter(limit int, w time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: w, hits: map[string]*window{}}
}

// Allow returns true when key is under its limit.
func (l *rateLimiter) Allow(key string) bool {
	return l.AllowN(key, l.limit)
}

// AllowN uses a per-call limit (settings-driven).
func (l *rateLimiter) AllowN(key string, limit int) bool {
	if limit <= 0 {
		limit = l.limit
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) >= 10000 { // crude GC
		for k, w := range l.hits {
			if now.Sub(w.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	w, ok := l.hits[key]
	if !ok && len(l.hits) >= 10000 {
		return false
	}
	if !ok || now.Sub(w.start) > l.window {
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= limit
}
