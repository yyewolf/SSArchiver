package archiver_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// ready adds Alice with the given scores already listed and backfill done,
// so the next Step goes straight to replay downloads. It returns the player ID.
func ready(t *testing.T, e *env, items []scoresaber.ScoreItem) string {
	t.Helper()
	ctx := context.Background()
	a := e.add(t, "1001")
	if _, err := e.svc.UpsertPlays(ctx, a, model.PlatformScoreSaber, scoresaber.Plays(items)); err != nil {
		t.Fatal(err)
	}
	_ = e.svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey(a))
	_ = e.svc.SetFeedBackfill(ctx, testutil.ScoreFeedKey(a), model.BackfillDone, 2, 1)
	e.fc.scores["1001"] = items
	return a
}

func TestDownloadArchives(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	if !e.step(t) {
		t.Fatal("expected a download")
	}
	s, _ := e.svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayArchived || s.ReplaySize != int64(len("replay-1")) || s.ReplaySHA256 == "" {
		t.Fatalf("score = %+v", s)
	}
	b, err := os.ReadFile(e.svc.Store().Path(storage.Loc{PlayerID: a, RowID: 1, Ext: ".dat"}))
	if err != nil || string(b) != "replay-1" {
		t.Fatalf("file = %q, %v", b, err)
	}
	if st := e.w.Status(); st.State != archiver.StateRunning || !strings.Contains(st.Task, "Downloading replay 1") {
		t.Fatalf("status = %+v", st)
	}
}

func TestDownload404MarksGone(t *testing.T) {
	e := newEnv(t)
	items := e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute))
	delete(e.fc.replays, 1)
	ready(t, e, items)
	e.step(t)
	s, _ := e.svc.GetScore(context.Background(), 1)
	if s.ReplayState != model.ReplayGone {
		t.Fatalf("state = %s", s.ReplayState)
	}
}

func TestDownloadTransientErrorsBackOffThenFail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	e.fc.replayErr[1] = &scoresaber.StatusError{StatusCode: 502}
	for i := 1; i <= service.MaxReplayAttempts; i++ {
		if !e.step(t) {
			t.Fatalf("attempt %d: no work found", i)
		}
		if i < service.MaxReplayAttempts && e.step(t) {
			t.Fatalf("attempt %d: retried before backoff elapsed", i)
		}
		e.clk.Advance(service.Backoff(i))
		_ = e.svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey(a)) // keep the poll from becoming due while time advances
	}
	s, _ := e.svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayFailed || s.Attempts != service.MaxReplayAttempts {
		t.Fatalf("score = %+v", s)
	}
}

func TestDownloadMidStreamFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	e.fc.replayReader[1] = func() io.ReadCloser {
		return io.NopCloser(io.MultiReader(strings.NewReader("ScoreSaber Replay partial"), iotest.ErrReader(errors.New("connection reset by peer"))))
	}
	e.step(t)
	s, _ := e.svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayPending || s.Attempts != 1 || !strings.Contains(s.LastError, "connection reset") {
		t.Fatalf("score = %+v", s)
	}
	dir := filepath.Dir(e.svc.Store().Path(storage.Loc{PlayerID: a, RowID: 1, Ext: ".dat"}))
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("leftover files after failed download: %v", entries)
	}
}

func TestDownloadRateLimitedDoesNotCountAttempt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	e.fc.replayErr[1] = fmt.Errorf("%w: test", scoresaber.ErrRateLimited)
	e.step(t)
	s, _ := e.svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayPending || s.Attempts != 0 || s.NextAttemptAt != nil {
		t.Fatalf("score = %+v", s)
	}
}

func TestStorageErrorPausesWorker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	e := newEnv(t)
	ctx := context.Background()
	a := ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	root := filepath.Dir(filepath.Dir(e.svc.Store().Path(storage.Loc{PlayerID: a, RowID: 1, Ext: ".dat"})))
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) })
	e.step(t)
	st, _ := e.svc.Settings(ctx)
	s, _ := e.svc.GetScore(ctx, 1)
	if !st.WorkerPaused || s.Attempts != 0 || s.ReplayState != model.ReplayPending {
		t.Fatalf("paused=%v score=%+v", st.WorkerPaused, s)
	}
}

func TestStepPriority(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.add(t, "1001")
	e.fc.perPage = 1
	newer := e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)) // after AddedAt → TierNew
	older := e.fc.history("1001", 2, 5, testutil.T0.Add(-time.Hour))  // before AddedAt → TierBackfill; 6 pages in total
	e.fc.scores["1001"] = append(newer, older...)

	e.step(t) // 1. poll (pages 1..5, backfill handed over at page 6)
	if got := e.fc.calls(); len(got) != 5 {
		t.Fatalf("step 1 should poll 5 pages, calls = %v", got)
	}
	e.fc.reset()
	e.step(t) // 2. new replay
	if got := e.fc.replaysCalled(); !slices.Equal(got, []int64{1}) {
		t.Fatalf("step 2 should download the new replay, got %v", got)
	}
	e.step(t) // 3. backfill listing (last page → backfill done)
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:6"}) {
		t.Fatalf("step 3 should list backfill page 6, got %v", got)
	}
	e.step(t) // 4. old replay (newest old first)
	if got := e.fc.replaysCalled(); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("step 4 should download replay 2, got %v", got)
	}
	_ = e.svc.RequestPoll(ctx, a)
	e.fc.reset()
	e.step(t) // 5. a due poll beats everything
	if got := e.fc.calls(); len(got) == 0 || got[0] != "1001:1" {
		t.Fatalf("step 5 should poll, got %v", got)
	}
}

func TestStatusReportsRateLimited(t *testing.T) {
	l := scoresaber.NewLimiter(300)
	l.Observe(http.Header{"Retry-After": {"60"}}, http.StatusTooManyRequests)
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(newFake(), l))
	st := archiver.New(svc).Status()
	if st.State != archiver.StateIdle || len(st.Limiters) != 1 || st.Limiters[0].Platform != "ScoreSaber" || st.Limiter.BlockedUntil.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}
