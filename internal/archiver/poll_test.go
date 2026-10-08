package archiver_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestFirstPollShortHistoryCompletesBackfill(t *testing.T) {
	e := newEnv(t)
	e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 3, testutil.T0.Add(-time.Hour))
	if !e.step(t) {
		t.Fatal("first step must poll")
	}
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:1", "1001:2"}) {
		t.Fatalf("calls = %v", got)
	}
	p, _ := e.svc.GetPlayer(context.Background(), "1001")
	if p.BackfillState != model.BackfillDone || p.LastPolledAt == nil {
		t.Fatalf("player = %+v", p)
	}
}

func TestFirstPollLongHistoryHandsOverToBackfill(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 20, testutil.T0.Add(-time.Hour))
	e.step(t)
	if got := e.fc.calls(); len(got) != 5 || got[4] != "1001:5" {
		t.Fatalf("first poll calls = %v", got)
	}
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.BackfillState != model.BackfillPending || p.BackfillPage != 6 || p.BackfillTotalPages != 10 {
		t.Fatalf("backfill = %s page %d/%d", p.BackfillState, p.BackfillPage, p.BackfillTotalPages)
	}
	e.fc.reset()
	e.drain(t, 100)
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:6", "1001:7", "1001:8", "1001:9", "1001:10"}) {
		t.Fatalf("backfill calls = %v", got)
	}
	p, _ = e.svc.GetPlayer(ctx, "1001")
	if p.BackfillState != model.BackfillDone {
		t.Fatalf("backfill state = %s", p.BackfillState)
	}
	c, _ := e.svc.PlayerCounts(ctx, "1001")
	if c.Scores != 20 {
		t.Fatalf("scores stored = %d", c.Scores)
	}
}

func TestPollStopsAtKnownScore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	old := e.fc.history("1001", 1, 3, testutil.T0.Add(-time.Hour))
	e.fc.scores["1001"] = old
	e.drain(t, 100)
	fresh := e.fc.history("1001", 50, 3, testutil.T0.Add(time.Hour))
	e.fc.scores["1001"] = append(fresh, old...)
	e.fc.reset()
	if err := e.svc.RequestPoll(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	e.step(t)
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:1", "1001:2"}) {
		t.Fatalf("poll must stop at the page containing a known score, calls = %v", got)
	}
	evs, _, _ := e.svc.ListEvents(ctx, service.EventFilter{Kind: model.KindScores})
	if len(evs) == 0 || !strings.Contains(evs[0].Message, "3 new scores") {
		t.Fatalf("expected a scores event, got %v", evs)
	}
}

func TestPollCapResumesBackfill(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	old := e.fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	e.fc.scores["1001"] = old
	e.drain(t, 100)
	e.fc.scores["1001"] = append(e.fc.history("1001", 100, 12, testutil.T0.Add(time.Hour)), old...)
	_ = e.svc.RequestPoll(ctx, "1001")
	e.fc.reset()
	e.step(t)
	if got := e.fc.calls(); len(got) != 5 {
		t.Fatalf("poll must stop at MaxPollPages, calls = %v", got)
	}
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.BackfillState != model.BackfillPending || p.BackfillPage != 6 {
		t.Fatalf("backfill not resumed: %s page %d", p.BackfillState, p.BackfillPage)
	}
}

func TestPollRateLimitedDoesNotMarkPolled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scoresErr["1001"] = fmt.Errorf("%w: test", scoresaber.ErrRateLimited)
	e.step(t)
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.LastPolledAt != nil {
		t.Fatal("a rate-limited poll must stay due")
	}
	evs, _, _ := e.svc.ListEvents(ctx, service.EventFilter{Kind: model.KindRateLimit})
	if len(evs) != 1 {
		t.Fatalf("rate limit events = %d", len(evs))
	}
}

func TestPollPlayerNotFoundDisables(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scoresErr["1001"] = fmt.Errorf("%w: gone", scoresaber.ErrNotFound)
	e.step(t)
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.Enabled || !strings.Contains(p.LastError, "not found") {
		t.Fatalf("player = %+v", p)
	}
}

func TestPollServerErrorRetriesNextInterval(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	_ = e.svc.SetBackfill(ctx, "1001", model.BackfillDone, 2, 1) // isolate polling from backfill work
	e.fc.scoresErr["1001"] = &scoresaber.StatusError{StatusCode: 502, Body: "bad gateway"}
	e.step(t)
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.LastPolledAt == nil || !strings.Contains(p.LastError, "502") || !p.Enabled {
		t.Fatalf("player = %+v", p)
	}
	if e.step(t) {
		t.Fatal("player must not be re-polled before the interval")
	}
}

func TestBackfillErrorDefers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 20, testutil.T0.Add(-time.Hour))
	e.step(t) // first poll → backfill pending at page 6
	e.fc.scoresErr["1001"] = &scoresaber.StatusError{StatusCode: 502}
	e.drain(t, 100) // the backfill page fails once and is deferred (Task 9 also drains replay downloads here)
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.BackfillRetryAt == nil || !p.BackfillRetryAt.Equal(e.clk.Now().Add(service.BackfillRetryDelay)) || !strings.Contains(p.LastError, "502") {
		t.Fatalf("backfill not deferred: %+v", p)
	}
	delete(e.fc.scoresErr, "1001")
	e.clk.Advance(service.BackfillRetryDelay)
	e.fc.reset()
	e.step(t)
	if got := e.fc.calls(); len(got) != 1 || got[0] != "1001:6" {
		t.Fatalf("deferred backfill not retried: %v", got)
	}
}
