package beatleader_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/beatleader"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func TestLimiterWindow(t *testing.T) {
	l := beatleader.NewAPILimiter()
	now := t0
	l.SetClock(func() time.Time { return now })
	ctx := context.Background()
	for range 40 {
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(100 * time.Millisecond)
	}
	if ok, at := l.Ready(now); ok || !at.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("41st request: ok=%v at=%v, want blocked until the first hit leaves the window", ok, at)
	}
	snap := l.Snapshot() // before the next Ready, which prunes the oldest hit
	if len(snap.Windows) != 1 || snap.Windows[0].Name != "short" || snap.Windows[0].Limit != 40 || snap.Windows[0].Used != 40 ||
		snap.Windows[0].ServerRemaining != -1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if ok, _ := l.Ready(t0.Add(10 * time.Second)); !ok {
		t.Fatal("ready once the oldest hit is 10s old")
	}
	if l.Name() != beatleader.APILimiterName || beatleader.NewCDNLimiter().Snapshot().Windows[0].Limit != 20 {
		t.Fatal("names and limits")
	}
}

func TestLimiterServerHeaders(t *testing.T) {
	l := beatleader.NewCDNLimiter()
	l.SetClock(func() time.Time { return t0 })
	h := http.Header{}
	h.Set("x-rate-limit-limit", "10s")
	h.Set("x-rate-limit-remaining", "12")
	h.Set("x-rate-limit-reset", "2026-10-10T12:00:04.6376402Z")
	l.Observe(h, 200)
	if ok, _ := l.Ready(t0); !ok {
		t.Fatal("remaining > 0 must not block")
	}
	if w := l.Snapshot().Windows[0]; w.ServerRemaining != 12 || !w.ServerResetAt.Equal(time.Date(2026, 10, 10, 12, 0, 4, 637640200, time.UTC)) {
		t.Fatalf("server window = %+v", w)
	}
	h.Set("x-rate-limit-remaining", "0")
	l.Observe(h, 200)
	if ok, at := l.Ready(t0); ok || !at.Equal(time.Date(2026, 10, 10, 12, 0, 4, 637640200, time.UTC)) {
		t.Fatalf("exhausted: ok=%v at=%v", ok, at)
	}

	l2 := beatleader.NewAPILimiter()
	l2.SetClock(func() time.Time { return t0 })
	l2.Observe(http.Header{}, http.StatusTooManyRequests)
	if ok, at := l2.Ready(t0); ok || !at.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("429 without headers waits one window: ok=%v at=%v", ok, at)
	}
	if l2.Snapshot().BlockedUntil.IsZero() {
		t.Fatal("snapshot must show the block")
	}
}

func TestLimiterWaitHonoursContext(t *testing.T) {
	l := beatleader.NewCDNLimiter()
	l.Observe(http.Header{}, http.StatusTooManyRequests) // blocked for 10s of real time
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("Wait must return the context error while blocked")
	}
	if l.Snapshot().Waiting {
		t.Fatal("Waiting must be cleared after a cancelled wait")
	}
}
