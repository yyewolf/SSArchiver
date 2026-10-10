package archiver_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// attempts returns n testplat attempts (newest first) one minute apart
// before T0, each with a replay.
func attempts(n int) []platform.Play {
	var out []platform.Play
	for i := range n {
		p := testutil.FakePlay(model.KindAttempt, fmt.Sprintf("a%02d", i), "lb-a", testutil.T0.Add(-time.Duration(i+1)*time.Minute), true)
		p.EndType = model.EndFail
		out = append(out, p)
	}
	return out
}

func (e *tpEnv) attemptFeed(t *testing.T, playerID string) model.SyncFeed {
	t.Helper()
	sum, err := e.svc.GetPlayerSummary(context.Background(), playerID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range sum.Identities {
		if id.Platform == "testplat" {
			return id.Feed(model.KindAttempt)
		}
	}
	t.Fatal("no testplat account")
	return model.SyncFeed{}
}

func (e *tpEnv) enableAttempts(t *testing.T, playerID string) service.FeedKey {
	t.Helper()
	k := service.FeedKey{PlayerID: playerID, Platform: "testplat", Kind: model.KindAttempt}
	if _, err := e.svc.SetFeedEnabled(context.Background(), k, true); err != nil {
		t.Fatal(err)
	}
	return k
}

func hasCall(calls []string, prefix string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func TestAccessProbeComesFirst(t *testing.T) {
	e := newTPEnv(t)
	ctx := context.Background()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	e.fp.SetPlays(model.KindAttempt, "abc", attempts(3)...)

	if did, err := e.w.Step(ctx); err != nil || !did {
		t.Fatalf("step = %v %v", did, err)
	}
	if len(e.fp.Probes) != 1 || len(e.fp.Calls) != 0 {
		t.Fatalf("the first unit of work is the probe: probes=%v calls=%v", e.fp.Probes, e.fp.Calls)
	}
	e.drain(t)
	if hasCall(e.fp.Calls, "attempt:") {
		t.Fatalf("a private feed is never listed: %v", e.fp.Calls)
	}
	if f := e.attemptFeed(t, tess); f.Access != model.AccessPrivate {
		t.Fatalf("feed = %+v", f)
	}
	if ev := e.events(t, model.KindPoll); !strings.Contains(ev, "warn: History is private") {
		t.Fatalf("events:\n%s", ev)
	}
}

func TestPrivateFeedRecheckedDaily(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	e.fp.SetPlays(model.KindAttempt, "abc", attempts(3)...)
	e.drain(t)

	e.clk.Advance(23 * time.Hour)
	e.drain(t)
	if len(e.fp.Probes) != 1 {
		t.Fatalf("no re-check before 24h: %v", e.fp.Probes)
	}
	e.fp.Access["attempt/abc"] = model.AccessPublic
	e.clk.Advance(2 * time.Hour)
	e.drain(t)
	if len(e.fp.Probes) != 2 {
		t.Fatalf("re-checked after 24h: %v", e.fp.Probes)
	}
	if c, _ := e.svc.GetPlayerSummary(context.Background(), tess); c.Identities[0].Counts[model.KindAttempt].Archived != 3 {
		t.Fatalf("once public, attempts are listed and archived: %+v", c.Identities[0].Counts)
	}
}

func TestAccessLostMidBackfill(t *testing.T) {
	e := newTPEnv(t)
	ctx := context.Background()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	e.fp.SetPlays(model.KindAttempt, "abc", attempts(14)...) // 7 pages: the first poll reads 5
	// Run until the attempt feed's first poll is done, then lose access.
	for range 50 {
		if f := e.attemptFeed(t, tess); f.LastPolledAt != nil {
			break
		}
		if _, err := e.w.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f := e.attemptFeed(t, tess); f.BackfillPage != 6 {
		t.Fatalf("expected a backfill to resume at page 6: %+v", f)
	}
	e.fp.FeedErr["attempt/abc"] = fmt.Errorf("%w: scoresstats", platform.ErrUnauthorized)
	e.drain(t)
	f := e.attemptFeed(t, tess)
	if f.Access != model.AccessPrivate || f.BackfillPage != 6 {
		t.Fatalf("access lost: %+v", f)
	}
	if c, _ := e.svc.GetPlayerSummary(ctx, tess); c.Identities[0].Counts[model.KindAttempt].Scores != 10 {
		t.Fatalf("rows are kept: %+v", c.Identities[0].Counts)
	}
	if ev := e.events(t, model.KindPoll); !strings.Contains(ev, "warn: History is private; paused until access is back") {
		t.Fatalf("events:\n%s", ev)
	}

	// Access comes back: the backfill resumes where it stopped.
	delete(e.fp.FeedErr, "attempt/abc")
	e.clk.Advance(25 * time.Hour)
	e.drain(t)
	if f := e.attemptFeed(t, tess); f.Access != model.AccessPublic || f.BackfillState != model.BackfillDone {
		t.Fatalf("resumed: %+v", f)
	}
	if c, _ := e.svc.GetPlayerSummary(ctx, tess); c.Identities[0].Counts[model.KindAttempt].Archived != 14 {
		t.Fatalf("all attempts archived: %+v", c.Identities[0].Counts)
	}
}

func TestReplayUnauthorizedPausesFeed(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	plays := attempts(1)
	e.fp.SetPlays(model.KindAttempt, "abc", plays...)
	e.fp.ReplayErr[plays[0].ReplayURL] = fmt.Errorf("%w: otherreplays", platform.ErrUnauthorized)
	e.drain(t)
	if f := e.attemptFeed(t, tess); f.Access != model.AccessPrivate {
		t.Fatalf("a 401 on a replay flips the feed: %+v", f)
	}
	sc := testutil.Row(t, e.svc, tess, "a00")
	if sc.ReplayState != model.ReplayPending || sc.Attempts != 0 {
		t.Fatalf("the replay waits for access, no failed attempt counted: %+v", sc)
	}
}

func TestUnauthorizedOnARequiredFeedIsAnError(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.fp.FeedErr["score/abc"] = fmt.Errorf("%w: scores", platform.ErrUnauthorized)
	e.drain(t)
	sum, _ := e.svc.GetPlayerSummary(context.Background(), tess)
	if f := sum.Identities[0].Feed(model.KindScore); f.Access != model.AccessNA || !strings.Contains(f.LastError, "unauthorized") {
		t.Fatalf("feeds without NeedsAccess are never made private: %+v", f)
	}
}

func TestAttemptWorkWaitsForScoreWork(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	var scores []platform.Play
	for i := range 12 { // 6 pages: page 6 is backfill work
		scores = append(scores, testutil.FakePlay(model.KindScore, fmt.Sprintf("s%02d", i), fmt.Sprintf("lb-%d", i), testutil.T0.Add(-time.Duration(i+1)*time.Hour), true))
	}
	e.fp.SetPlays(model.KindScore, "abc", scores...)
	e.fp.SetPlays(model.KindAttempt, "abc", attempts(12)...)
	e.drain(t)

	idx := func(calls []string, match func(string) bool, last bool) int {
		i := -1
		for j, c := range calls {
			if match(c) {
				if !last {
					return j
				}
				i = j
			}
		}
		return i
	}
	isScoreReplay := func(u string) bool { return strings.Contains(u, "/s") }
	isAttemptReplay := func(u string) bool { return strings.Contains(u, "/a") }
	if lastScore, firstAttempt := idx(e.fp.ReplayCalls, isScoreReplay, true), idx(e.fp.ReplayCalls, isAttemptReplay, false); lastScore < 0 || firstAttempt < lastScore {
		t.Fatalf("every score replay before any attempt replay: %v", e.fp.ReplayCalls)
	}
	if s6, a6 := slices.Index(e.fp.Calls, "score:abc:6"), slices.Index(e.fp.Calls, "attempt:abc:6"); s6 < 0 || a6 < s6 {
		t.Fatalf("score backfill listing before attempt backfill listing: %v", e.fp.Calls)
	}
}
