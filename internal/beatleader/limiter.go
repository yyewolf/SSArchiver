package beatleader

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/platform"
)

// Limiter names (spec §5.3): one per host class.
const (
	APILimiterName = "beatleader/api"
	CDNLimiterName = "beatleader/cdn"
)

// window is BeatLeader's rate-limit window (x-rate-limit-limit: 10s).
const window = 10 * time.Second

// Limiter is a sliding-window client-side limiter for one BeatLeader host
// class. It also honours the server's x-rate-limit-remaining/-reset headers.
type Limiter struct {
	mu              sync.Mutex
	name            string
	limit           int
	now             func() time.Time
	hits            []time.Time
	serverRemaining int
	serverResetAt   time.Time
	blockedUntil    time.Time
	waitUntil       time.Time
}

// NewAPILimiter limits api.beatleader.xyz: 40 requests per 10 s, headroom
// under the 50 observed.
func NewAPILimiter() *Limiter { return newLimiter(APILimiterName, 40) }

// NewCDNLimiter limits cdn.replays.beatleader.xyz downloads: 20 per 10 s.
func NewCDNLimiter() *Limiter { return newLimiter(CDNLimiterName, 20) }

func newLimiter(name string, limit int) *Limiter {
	return &Limiter{name: name, limit: limit, now: time.Now, serverRemaining: -1}
}

// SetClock replaces the time source (tests).
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

func (l *Limiter) Name() string { return l.name }

// prune drops hits older than the window; l.mu must be held.
func (l *Limiter) prune(now time.Time) {
	i := 0
	for i < len(l.hits) && !l.hits[i].Add(window).After(now) {
		i++
	}
	l.hits = l.hits[i:]
}

// nextAllowed is when the next request may go out; l.mu must be held.
func (l *Limiter) nextAllowed(now time.Time) time.Time {
	l.prune(now)
	until := l.blockedUntil
	if len(l.hits) >= l.limit {
		if t := l.hits[0].Add(window); t.After(until) {
			until = t
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

// Wait blocks until a request may be sent, then records it.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := l.now()
		until := l.nextAllowed(now)
		if !until.After(now) {
			l.hits = append(l.hits, now)
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

// Observe records BeatLeader's rate-limit headers. An exhausted window or a
// 429 blocks until x-rate-limit-reset (Retry-After or one window when absent).
func (l *Limiter) Observe(h http.Header, status int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var reset time.Time
	if v := h.Get("x-rate-limit-reset"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			reset = t.UTC()
			l.serverResetAt = reset
		}
	}
	if v := h.Get("x-rate-limit-remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			l.serverRemaining = n
			if n <= 0 && reset.After(l.blockedUntil) {
				l.blockedUntil = reset
			}
		}
	}
	if status == http.StatusTooManyRequests && !l.blockedUntil.After(now) {
		until := now.Add(window)
		if reset.After(now) {
			until = reset
		}
		if ra, err := strconv.Atoi(h.Get("Retry-After")); err == nil && ra > 0 {
			until = now.Add(time.Duration(ra) * time.Second)
		}
		l.blockedUntil = until
	}
}

// Snapshot returns the current usage for display.
func (l *Limiter) Snapshot() platform.LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	s := platform.LimiterSnapshot{
		Waiting: !l.waitUntil.IsZero(), WaitUntil: l.waitUntil,
		Windows: []platform.WindowSnapshot{{
			Name: "short", Limit: l.limit, Used: len(l.hits), Period: window,
			ServerRemaining: l.serverRemaining, ServerResetAt: l.serverResetAt,
		}},
	}
	if l.blockedUntil.After(now) {
		s.BlockedUntil = l.blockedUntil
	}
	return s
}
