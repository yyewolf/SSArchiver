package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func attemptKey(playerID string) service.FeedKey {
	return service.FeedKey{PlayerID: playerID, Platform: "testplat", Kind: model.KindAttempt}
}

func pollEvents(t *testing.T, svc *service.Service) string {
	t.Helper()
	evs, _, err := svc.ListEvents(context.Background(), service.EventFilter{Kind: model.KindPoll, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(e.Level + ": " + e.Message + "\n")
	}
	return b.String()
}

func TestSetFeedEnabled(t *testing.T) {
	svc, _, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	<-svc.WakeC()
	k := attemptKey(tess)
	clk.Advance(time.Hour)

	f, err := svc.SetFeedEnabled(ctx, k, true)
	if err != nil || !f.Enabled || f.Access != model.AccessUnknown || !f.StartedAt.Equal(testutil.T0.Add(time.Hour)) ||
		f.BackfillState != model.BackfillPending || f.BackfillPage != 1 {
		t.Fatalf("enabled feed = %+v %v", f, err)
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("switching a feed on must wake the worker")
	}
	if err := svc.SetFeedBackfill(ctx, k, model.BackfillRunning, 4, 9); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.SetFeedEnabled(ctx, k, false); f.Enabled || f.BackfillPage != 4 {
		t.Fatalf("switching off keeps the cursor: %+v", f)
	}
	clk.Advance(time.Hour)
	f, _ = svc.SetFeedEnabled(ctx, k, true)
	if !f.Enabled || f.BackfillPage != 4 || !f.StartedAt.Equal(testutil.T0.Add(2*time.Hour)) || f.Access != model.AccessUnknown {
		t.Fatalf("switching on again restarts the clock and the access check, not the cursor: %+v", f)
	}

	for _, bad := range []struct {
		k    service.FeedKey
		want error
	}{
		{service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindScore}, service.ErrFeedNotOptional},
		{service.FeedKey{PlayerID: tess, Platform: model.PlatformScoreSaber, Kind: model.KindAttempt}, service.ErrNotFound},
		{service.FeedKey{PlayerID: tess, Platform: "nope", Kind: model.KindAttempt}, service.ErrNotFound},
		{attemptKey("p99999999999"), service.ErrNotFound},
	} {
		if _, err := svc.SetFeedEnabled(ctx, bad.k, true); !errors.Is(err, bad.want) {
			t.Errorf("%v: %v, want %v", bad.k, err, bad.want)
		}
	}

	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "def", "testplat"); err != nil {
		t.Fatal(err)
	}
	if f, err := svc.SetFeedEnabled(ctx, attemptKey(alice), false); err != nil || f.Enabled {
		t.Fatalf("switching off a feed that was never on = %+v %v", f, err)
	}
	if feeds, _ := svc.Feeds(ctx, alice); len(feeds) != 2 {
		t.Fatalf("it stores nothing: %d feeds", len(feeds))
	}
}

func TestCheckFeedAccess(t *testing.T) {
	svc, fp, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	k := attemptKey(tess)
	if _, err := svc.SetFeedEnabled(ctx, k, true); err != nil {
		t.Fatal(err)
	}

	fp.Access["attempt/abc"] = model.AccessPrivate
	f, err := svc.CheckFeedAccess(ctx, k)
	if err != nil || f.Access != model.AccessPrivate || f.AccessCheckedAt == nil || !f.AccessCheckedAt.Equal(testutil.T0) {
		t.Fatalf("private = %+v %v", f, err)
	}
	_, _ = svc.CheckFeedAccess(ctx, k)
	if ev := pollEvents(t, svc); strings.Count(ev, "warn: History is private") != 1 {
		t.Fatalf("one warning when access turns private, not one per check:\n%s", ev)
	}

	fp.Access["attempt/abc"] = model.AccessPublic
	fp.AccessTotal = 42
	clk.Advance(time.Minute)
	f, _ = svc.CheckFeedAccess(ctx, k)
	if f.Access != model.AccessPublic || f.RemoteTotal != 42 || !f.AccessCheckedAt.Equal(testutil.T0.Add(time.Minute)) {
		t.Fatalf("public = %+v", f)
	}
	if ev := pollEvents(t, svc); !strings.Contains(ev, "info: TestPlat attempt history is public: archiving it") {
		t.Fatalf("events:\n%s", ev)
	}

	fp.ProbeErr = errors.New("boom")
	clk.Advance(time.Minute)
	f, err = svc.CheckFeedAccess(ctx, k)
	if err == nil || f == nil || f.Access != model.AccessPublic || f.LastError != "access check failed: boom" ||
		!f.AccessCheckedAt.Equal(testutil.T0.Add(2*time.Minute)) {
		t.Fatalf("failed probe = %+v %v", f, err)
	}
	fp.ProbeErr = fmt.Errorf("%w: slow down", platform.ErrRateLimited)
	clk.Advance(time.Minute)
	if f, err := svc.CheckFeedAccess(ctx, k); !errors.Is(err, platform.ErrRateLimited) || !f.AccessCheckedAt.Equal(testutil.T0.Add(2*time.Minute)) {
		t.Fatalf("a rate-limited probe records nothing: %+v %v", f, err)
	}

	probes := len(fp.Probes)
	score := service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindScore}
	if f, err := svc.CheckFeedAccess(ctx, score); err != nil || f.Access != model.AccessNA || len(fp.Probes) != probes {
		t.Fatalf("feeds without NeedsAccess are not probed: %+v %v", f, err)
	}
	if _, err := svc.CheckFeedAccess(ctx, attemptKey("p99999999999")); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown feed = %v", err)
	}
}

func TestDueProbe(t *testing.T) {
	svc, fp, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	k := attemptKey(tess)
	due := func() *service.WorkFeed {
		t.Helper()
		f, err := svc.DueProbe(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if due() != nil {
		t.Fatal("no optional feed yet")
	}
	_, _ = svc.SetFeedEnabled(ctx, k, true)
	if f := due(); f == nil || f.Key() != k || f.ExternalID != "abc" || f.PlayerName != "Tess" {
		t.Fatalf("an unknown feed is probed at once: %+v", f)
	}
	if f, _ := svc.DueProbe(ctx, service.Busy{{Platform: "testplat", Kind: model.KindAttempt}: true}); f != nil {
		t.Fatal("busy platforms are skipped")
	}

	fp.Access["attempt/abc"] = model.AccessPrivate
	_, _ = svc.CheckFeedAccess(ctx, k)
	if due() != nil {
		t.Fatal("a private feed waits for the daily re-check")
	}
	clk.Advance(23 * time.Hour)
	if due() != nil {
		t.Fatal("not yet")
	}
	clk.Advance(2 * time.Hour)
	if due() == nil {
		t.Fatal("re-checked after 24h")
	}
	fp.Access["attempt/abc"] = model.AccessPublic
	_, _ = svc.CheckFeedAccess(ctx, k)
	if due() != nil {
		t.Fatal("a public feed is not probed")
	}

	_, _ = svc.SetFeedEnabled(ctx, k, false)
	_, _ = svc.SetFeedEnabled(ctx, k, true)
	fp.ProbeErr = errors.New("boom")
	_, _ = svc.CheckFeedAccess(ctx, k)
	if due() != nil {
		t.Fatal("a failed probe waits before retrying")
	}
	clk.Advance(6 * time.Minute)
	if due() == nil {
		t.Fatal("retried after 5 minutes")
	}
	_ = svc.SetIdentityEnabled(ctx, tess, "testplat", false)
	if due() != nil {
		t.Fatal("paused accounts are not probed")
	}
}

func TestOnlyAccessibleFeedsAreWorked(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	k := attemptKey(tess)
	_, _ = svc.SetFeedEnabled(ctx, k, true)
	_ = svc.MarkFeedPolled(ctx, service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindScore})
	testutil.UpsertFake(t, svc, tess, testutil.FakePlay(model.KindAttempt, "a1", "lb-a", testutil.T0.Add(-time.Hour), true))

	work := func() (*service.WorkFeed, *model.Score) {
		t.Helper()
		f, err := svc.DueFeed(ctx, time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		sc, err := svc.NextReplay(ctx, service.TierBackfillOther, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		return f, sc
	}
	if f, sc := work(); f != nil || sc != nil {
		t.Fatalf("access unknown: no work (%+v, %+v)", f, sc)
	}
	if _, err := svc.CheckFeedAccess(ctx, k); err != nil {
		t.Fatal(err)
	}
	if f, sc := work(); f == nil || f.Key() != k || sc == nil || sc.ExternalID != "a1" {
		t.Fatalf("public: polled and downloaded (%+v, %+v)", f, sc)
	}
	if err := svc.MarkFeedPrivate(ctx, k); err != nil {
		t.Fatal(err)
	}
	if f, sc := work(); f != nil || sc != nil {
		t.Fatalf("private: no work (%+v, %+v)", f, sc)
	}
	if fs, _ := svc.Feeds(ctx, tess); fs[0].Access != model.AccessPrivate && fs[1].Access != model.AccessPrivate {
		t.Fatalf("feeds = %+v", fs)
	}
}
