package archiver_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestFirstPollShortHistoryCompletesBackfill(t *testing.T) {
	e := newEnv(t)
	a := e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 3, testutil.T0.Add(-time.Hour))
	if !e.step(t) {
		t.Fatal("first step must poll")
	}
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:1", "1001:2"}) {
		t.Fatalf("calls = %v", got)
	}
	f := testutil.ScoreFeed(t, e.svc, a)
	if f.BackfillState != model.BackfillDone || f.LastPolledAt == nil {
		t.Fatalf("feed = %+v", f)
	}
}

func TestFirstPollLongHistoryHandsOverToBackfill(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 20, testutil.T0.Add(-time.Hour))
	e.step(t)
	if got := e.fc.calls(); len(got) != 5 || got[4] != "1001:5" {
		t.Fatalf("first poll calls = %v", got)
	}
	f := testutil.ScoreFeed(t, e.svc, a)
	if f.BackfillState != model.BackfillPending || f.BackfillPage != 6 || f.BackfillTotalPages != 10 {
		t.Fatalf("backfill = %s page %d/%d", f.BackfillState, f.BackfillPage, f.BackfillTotalPages)
	}
	e.fc.reset()
	e.drain(t, 100)
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:6", "1001:7", "1001:8", "1001:9", "1001:10"}) {
		t.Fatalf("backfill calls = %v", got)
	}
	f = testutil.ScoreFeed(t, e.svc, a)
	if f.BackfillState != model.BackfillDone {
		t.Fatalf("backfill state = %s", f.BackfillState)
	}
	c, _ := e.svc.PlayerCounts(ctx, a)
	if c.Scores != 20 {
		t.Fatalf("scores stored = %d", c.Scores)
	}
}

func TestPollStopsAtKnownScore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.add(t, "1001")
	old := e.fc.history("1001", 1, 3, testutil.T0.Add(-time.Hour))
	e.fc.scores["1001"] = old
	e.drain(t, 100)
	fresh := e.fc.history("1001", 50, 3, testutil.T0.Add(time.Hour))
	e.fc.scores["1001"] = append(fresh, old...)
	e.fc.reset()
	if err := e.svc.RequestPoll(ctx, a); err != nil {
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
	a := e.add(t, "1001")
	old := e.fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	e.fc.scores["1001"] = old
	e.drain(t, 100)
	e.fc.scores["1001"] = append(e.fc.history("1001", 100, 12, testutil.T0.Add(time.Hour)), old...)
	_ = e.svc.RequestPoll(ctx, a)
	e.fc.reset()
	e.step(t)
	if got := e.fc.calls(); len(got) != 5 {
		t.Fatalf("poll must stop at MaxPollPages, calls = %v", got)
	}
	f := testutil.ScoreFeed(t, e.svc, a)
	if f.BackfillState != model.BackfillPending || f.BackfillPage != 6 {
		t.Fatalf("backfill not resumed: %s page %d", f.BackfillState, f.BackfillPage)
	}
}

func TestPollRateLimitedDoesNotMarkPolled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.add(t, "1001")
	e.fc.scoresErr["1001"] = fmt.Errorf("%w: test", scoresaber.ErrRateLimited)
	e.step(t)
	f := testutil.ScoreFeed(t, e.svc, a)
	if f.LastPolledAt != nil {
		t.Fatal("a rate-limited poll must stay due")
	}
	evs, _, _ := e.svc.ListEvents(ctx, service.EventFilter{Kind: model.KindRateLimit})
	if len(evs) != 1 {
		t.Fatalf("rate limit events = %d", len(evs))
	}
}

func TestPollPlayerNotFoundDisablesTheAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.add(t, "1001")
	e.fc.scoresErr["1001"] = fmt.Errorf("%w: gone", scoresaber.ErrNotFound)
	e.step(t)
	sum, err := e.svc.GetPlayerSummary(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Enabled || sum.Identities[0].Enabled || !strings.Contains(sum.Error(), "not found") {
		t.Fatalf("summary = %+v", sum)
	}
	if e.step(t) {
		t.Fatal("a disabled account must not be polled again")
	}
}

func TestPollServerErrorRetriesNextInterval(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.add(t, "1001")
	_ = e.svc.SetFeedBackfill(ctx, testutil.ScoreFeedKey(a), model.BackfillDone, 2, 1) // isolate polling from backfill work
	e.fc.scoresErr["1001"] = &scoresaber.StatusError{StatusCode: 502, Body: "bad gateway"}
	e.step(t)
	f := testutil.ScoreFeed(t, e.svc, a)
	if f.LastPolledAt == nil || !strings.Contains(f.LastError, "502") {
		t.Fatalf("feed = %+v", f)
	}
	if e.step(t) {
		t.Fatal("player must not be re-polled before the interval")
	}
}

func TestPollEventsCarryPlatformAndFeed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	e.step(t)
	evs, _, _ := e.svc.ListEvents(ctx, service.EventFilter{Kind: model.KindScores})
	if len(evs) != 1 || evs[0].Platform == nil || *evs[0].Platform != model.PlatformScoreSaber || evs[0].Feed == nil || *evs[0].Feed != model.KindScore {
		t.Fatalf("events = %+v", evs)
	}
}

func TestBackfillErrorDefers(t *testing.T) {
	e := newEnv(t)
	a := e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 20, testutil.T0.Add(-time.Hour))
	e.step(t) // first poll → backfill pending at page 6
	e.fc.scoresErr["1001"] = &scoresaber.StatusError{StatusCode: 502}
	e.drain(t, 100) // the backfill page fails once and is deferred (Task 9 also drains replay downloads here)
	f := testutil.ScoreFeed(t, e.svc, a)
	if f.BackfillRetryAt == nil || !f.BackfillRetryAt.Equal(e.clk.Now().Add(service.BackfillRetryDelay)) || !strings.Contains(f.LastError, "502") {
		t.Fatalf("backfill not deferred: %+v", f)
	}
	delete(e.fc.scoresErr, "1001")
	e.clk.Advance(service.BackfillRetryDelay)
	e.fc.reset()
	e.step(t)
	if got := e.fc.calls(); len(got) != 1 || got[0] != "1001:6" {
		t.Fatalf("deferred backfill not retried: %v", got)
	}
}

func TestWorkerPollsAccountIDNotPlayerID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := testutil.AddPlayer(t, e.svc, "1001")
	if id == "1001" {
		t.Fatal("test IDs must differ from account IDs")
	}
	e.fc.scores["1001"] = e.fc.history("1001", 1, 3, testutil.T0.Add(-time.Hour))
	e.step(t)
	if got := e.fc.calls(); len(got) == 0 || got[0] != "1001:1" {
		t.Fatalf("worker must call ScoreSaber with the account ID, calls = %v", got)
	}
	c, _ := e.svc.PlayerCounts(ctx, id)
	if c.Scores != 3 {
		t.Fatalf("scores must be stored under the player ID, got %d", c.Scores)
	}
}

func TestLegacyPlayerWithDifferentAccountID(t *testing.T) {
	fc := newFake()
	svc, gdb, clk := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil))
	e := &env{svc: svc, clk: clk, fc: fc, w: archiver.New(svc)}
	ctx := context.Background()
	// A legacy-style player whose ID is digits but is not its ScoreSaber ID
	// (e.g. relinked after an unlink): the worker must use the account ID.
	now := testutil.T0
	for _, row := range []any{
		&model.Player{ID: "123", Name: "Legacy", Enabled: true, AddedAt: now},
		&model.PlayerPlatform{PlayerID: "123", Platform: model.PlatformScoreSaber, ExternalID: "1001", Enabled: true, LinkedAt: now},
		&model.SyncFeed{
			PlayerID: "123", Platform: model.PlatformScoreSaber, Feed: model.KindScore, Enabled: true, StartedAt: now,
			Access: model.AccessNA, BackfillState: model.BackfillPending, BackfillPage: 1,
		},
	} {
		if err := gdb.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	fc.scores["1001"] = fc.history("1001", 1, 2, now.Add(-time.Hour))
	e.drain(t, 50)
	for _, c := range fc.calls() {
		if strings.HasPrefix(c, "123:") {
			t.Fatalf("worker called ScoreSaber with the player ID: %v", fc.calls())
		}
	}
	c, _ := svc.PlayerCounts(ctx, "123")
	if c.Scores != 2 || c.Archived != 2 {
		t.Fatalf("counts = %+v", c)
	}
}
