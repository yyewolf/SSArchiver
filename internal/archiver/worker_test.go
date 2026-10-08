package archiver_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
)

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 3s")
}

func TestPausedWorkerDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.add(t, "1001")
	if err := e.svc.SetWorkerPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if e.step(t) {
		t.Fatal("paused worker reported work")
	}
	if len(e.fc.calls()) != 0 {
		t.Fatal("paused worker called ScoreSaber")
	}
	if st := e.w.Status(); st.State != archiver.StatePaused {
		t.Fatalf("state = %s", st.State)
	}
}

func TestRunWakesAndStops(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.w.Run(ctx) }()
	eventually(t, func() bool { return e.w.Status().State == archiver.StateIdle })
	e.add(t, "1001") // AddPlayer wakes the worker
	eventually(t, func() bool { return len(e.fc.calls()) > 0 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if st := e.w.Status(); st.State != archiver.StateStopped {
		t.Fatalf("state after stop = %s", st.State)
	}
}
