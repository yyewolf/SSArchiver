package scoresaber

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func waitN(t *testing.T, l *Limiter, n int) {
	t.Helper()
	for range n {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShortWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		start := time.Now()
		waitN(t, l, 20)
		if d := time.Since(start); d != 0 {
			t.Fatalf("first 20 waited %v", d)
		}
		waitN(t, l, 1)
		if d := time.Since(start); d != 10*time.Second {
			t.Fatalf("21st waited until %v, want 10s", d)
		}
	})
}

func TestMediumWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		start := time.Now()
		waitN(t, l, 60) // 20 at 0s, 20 at 10s, 20 at 20s
		if d := time.Since(start); d != 20*time.Second {
			t.Fatalf("60th at %v, want 20s", d)
		}
		waitN(t, l, 1)
		if d := time.Since(start); d != 60*time.Second {
			t.Fatalf("61st at %v, want 60s", d)
		}
	})
}

func TestHourlyBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(3)
		start := time.Now()
		waitN(t, l, 4)
		if d := time.Since(start); d != time.Hour {
			t.Fatalf("4th at %v, want 1h", d)
		}
	})
}

func TestObserveRemainingZeroBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		h := http.Header{}
		h.Set("x-ratelimit-remaining-medium", "0")
		h.Set("x-ratelimit-reset-medium", "42")
		h.Set("x-ratelimit-remaining-long", "100")
		h.Set("x-ratelimit-reset-long", "3600")
		l.Observe(h, http.StatusOK)
		start := time.Now()
		waitN(t, l, 1)
		if d := time.Since(start); d != 42*time.Second {
			t.Fatalf("waited %v, want 42s", d)
		}
	})
}

func TestObserve429WithoutHeadersBacksOffOneMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		l.Observe(http.Header{}, http.StatusTooManyRequests)
		start := time.Now()
		waitN(t, l, 1)
		if d := time.Since(start); d != time.Minute {
			t.Fatalf("waited %v, want 1m", d)
		}
	})
}

func TestObserve429UsesExhaustedWindowNotLongReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		h := http.Header{}
		h.Set("x-ratelimit-remaining-short", "0")
		h.Set("x-ratelimit-reset-short", "7")
		h.Set("x-ratelimit-remaining-long", "250")
		h.Set("x-ratelimit-reset-long", "3600")
		l.Observe(h, http.StatusTooManyRequests)
		start := time.Now()
		waitN(t, l, 1)
		if d := time.Since(start); d != 7*time.Second {
			t.Fatalf("waited %v, want 7s", d)
		}
	})
}

func TestObserve429RetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		h := http.Header{}
		h.Set("Retry-After", "15")
		l.Observe(h, http.StatusTooManyRequests)
		start := time.Now()
		waitN(t, l, 1)
		if d := time.Since(start); d != 15*time.Second {
			t.Fatalf("waited %v, want 15s", d)
		}
	})
}

func TestWaitHonoursContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		l.Observe(http.Header{}, http.StatusTooManyRequests) // blocked 1m
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := l.Wait(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want deadline exceeded", err)
		}
		if s := l.Snapshot(); s.Waiting {
			t.Fatal("snapshot still reports waiting after cancellation")
		}
	})
}

func TestLimiterReady(t *testing.T) {
	l := NewLimiter(300)
	if l.Name() != "scoresaber" {
		t.Fatalf("Name = %q", l.Name())
	}
	if ok, _ := l.Ready(time.Now()); !ok {
		t.Fatal("fresh limiter must be ready")
	}
	ctx := context.Background()
	for range 20 { // the short window allows 20 per 10 s
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ok, at := l.Ready(now)
	if ok || !at.After(now) || at.After(now.Add(10*time.Second)) {
		t.Fatalf("full short window: Ready = %v %v", ok, at)
	}
}

func TestSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		waitN(t, l, 5)
		h := http.Header{}
		h.Set("x-ratelimit-remaining-long", "290")
		h.Set("x-ratelimit-reset-long", "1200")
		l.Observe(h, http.StatusOK)
		s := l.Snapshot()
		if len(s.Windows) != 3 {
			t.Fatalf("windows = %d", len(s.Windows))
		}
		for _, w := range s.Windows {
			if w.Used != 5 {
				t.Fatalf("%s used = %d, want 5", w.Name, w.Used)
			}
		}
		long := s.Windows[2]
		if long.Name != "long" || long.Limit != 300 || long.ServerRemaining != 290 || !long.ServerResetAt.Equal(time.Now().Add(20*time.Minute)) {
			t.Fatalf("long window snapshot wrong: %+v", long)
		}
		if s.Windows[0].ServerRemaining != -1 {
			t.Fatal("unknown server remaining must be -1")
		}
		time.Sleep(11 * time.Second)
		if used := l.Snapshot().Windows[0].Used; used != 0 {
			t.Fatalf("short window not pruned: %d", used)
		}
	})
}
