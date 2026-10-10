// Package scoresaber is a minimal client for ScoreSaber's public v2 API with
// a client-side rate limiter that mirrors the server's limits.
package scoresaber

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

type (
	WindowSnapshot  = platform.WindowSnapshot
	LimiterSnapshot = platform.LimiterSnapshot
)

type window struct {
	name            string
	limit           int
	period          time.Duration
	hits            []time.Time
	serverRemaining int
	serverResetAt   time.Time
}

func (w *window) prune(now time.Time) {
	i := 0
	for i < len(w.hits) && !w.hits[i].Add(w.period).After(now) {
		i++
	}
	w.hits = w.hits[i:]
}

// Limiter enforces ScoreSaber's three request windows for this process.
type Limiter struct {
	mu           sync.Mutex
	windows      []*window
	blockedUntil time.Time
	waitUntil    time.Time
}

// NewLimiter returns a limiter with ScoreSaber's short/medium windows and the
// given hourly budget for the long window.
func NewLimiter(hourly int) *Limiter {
	return &Limiter{windows: []*window{
		{name: "short", limit: 20, period: 10 * time.Second, serverRemaining: -1},
		{name: "medium", limit: 60, period: time.Minute, serverRemaining: -1},
		{name: "long", limit: hourly, period: time.Hour, serverRemaining: -1},
	}}
}

// Name identifies the limiter on the status page and in readiness checks.
func (l *Limiter) Name() string { return model.PlatformScoreSaber }

// nextAllowed is when the next request may go out; l.mu must be held.
func (l *Limiter) nextAllowed(now time.Time) time.Time {
	until := l.blockedUntil
	for _, w := range l.windows {
		w.prune(now)
		if len(w.hits) >= w.limit {
			if t := w.hits[0].Add(w.period); t.After(until) {
				until = t
			}
		}
	}
	return until
}

// Ready reports whether Wait would return at once at now, and otherwise when it could.
func (l *Limiter) Ready(now time.Time) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until := l.nextAllowed(now); until.After(now) {
		return false, until
	}
	return true, time.Time{}
}

// Wait blocks until a request may be sent, then records it in every window.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		until := l.nextAllowed(now)
		if !until.After(now) {
			for _, w := range l.windows {
				w.hits = append(w.hits, now)
			}
			l.waitUntil = time.Time{}
			l.mu.Unlock()
			return nil
		}
		l.waitUntil = until
		l.mu.Unlock()

		timer := time.NewTimer(until.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			l.mu.Lock()
			l.waitUntil = time.Time{}
			l.mu.Unlock()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Observe records rate-limit headers from a ScoreSaber response.
func (l *Limiter) Observe(h http.Header, status int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, w := range l.windows {
		reset, hasReset := headerInt(h, "x-ratelimit-reset-"+w.name)
		if hasReset {
			w.serverResetAt = resetTime(now, reset)
		}
		if rem, ok := headerInt(h, "x-ratelimit-remaining-"+w.name); ok {
			w.serverRemaining = int(rem)
			if rem <= 0 && hasReset && w.serverResetAt.After(l.blockedUntil) {
				l.blockedUntil = w.serverResetAt
			}
		}
	}
	if status == http.StatusTooManyRequests && !l.blockedUntil.After(now) {
		d := time.Minute
		if ra, ok := headerInt(h, "Retry-After"); ok && ra > 0 {
			d = time.Duration(ra) * time.Second
		}
		l.blockedUntil = now.Add(d)
	}
}

// Snapshot returns the current usage for display.
func (l *Limiter) Snapshot() LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	s := LimiterSnapshot{Waiting: !l.waitUntil.IsZero(), WaitUntil: l.waitUntil}
	if l.blockedUntil.After(now) {
		s.BlockedUntil = l.blockedUntil
	}
	for _, w := range l.windows {
		w.prune(now)
		s.Windows = append(s.Windows, WindowSnapshot{
			Name: w.name, Limit: w.limit, Used: len(w.hits), Period: w.period,
			ServerRemaining: w.serverRemaining, ServerResetAt: w.serverResetAt,
		})
	}
	return s
}

func headerInt(h http.Header, key string) (int64, bool) {
	v := h.Get(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil
}

func resetTime(now time.Time, v int64) time.Time {
	if v > 1_000_000_000 {
		return time.Unix(v, 0).UTC()
	}
	return now.Add(time.Duration(v) * time.Second)
}
