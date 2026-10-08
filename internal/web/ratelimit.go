package web

import (
	"sync"
	"time"
)

// loginLimiter allows `limit` attempts per key per `window`.
type loginLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{hits: map[string][]time.Time{}, limit: 5, window: time.Minute}
}

func (l *loginLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10000 { // bound memory under abuse
		l.hits = map[string][]time.Time{}
	}
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
