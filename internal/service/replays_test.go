package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func seedQueue(t *testing.T) (*service.Service, *testutil.Clock, string, string) {
	t.Helper()
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	a := mustAdd(t, svc, "1001") // added at T0
	b := mustAdd(t, svc, "1002")
	items := map[string][]scoresaber.ScoreItem{
		a: {
			testutil.Item("1001", 1, 11, testutil.T0.Add(time.Minute), true),
			testutil.Item("1001", 2, 12, testutil.T0.Add(2*time.Minute), true),
			testutil.Item("1001", 3, 13, testutil.T0.Add(-time.Hour), true),
		},
		b: {
			testutil.Item("1002", 4, 14, testutil.T0.Add(3*time.Minute), true),
			testutil.Item("1002", 5, 15, testutil.T0.Add(-2*time.Hour), true),
		},
	}
	for p, list := range items {
		if _, err := svc.UpsertPlays(ctx, p, model.PlatformScoreSaber, scoresaber.Plays(list)); err != nil {
			t.Fatal(err)
		}
	}
	return svc, clk, a, b
}

func nextID(t *testing.T, svc *service.Service, tier service.ReplayTier, last string) int64 {
	t.Helper()
	s, err := svc.NextReplay(context.Background(), tier, last, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		return 0
	}
	if s.Leaderboard == nil || s.Player == nil {
		t.Fatal("NextReplay must preload leaderboard and player")
	}
	return s.ID
}

func TestNextReplayTiersAndRoundRobin(t *testing.T) {
	svc, clk, a, b := seedQueue(t)
	ctx := context.Background()
	if got := nextID(t, svc, service.TierNew, ""); got != 2 {
		t.Fatalf("new/'' = %d, want 2 (Alice newest)", got)
	}
	if got := nextID(t, svc, service.TierNew, a); got != 4 {
		t.Fatalf("new/after Alice = %d, want 4 (Bob)", got)
	}
	if got := nextID(t, svc, service.TierNew, b); got != 2 {
		t.Fatalf("new/after Bob = %d, want 2 (wrap to Alice)", got)
	}
	if got := nextID(t, svc, service.TierBackfill, ""); got != 3 {
		t.Fatalf("backfill/'' = %d, want 3", got)
	}
	if err := svc.SetPlayerEnabled(ctx, a, false); err != nil {
		t.Fatal(err)
	}
	if got := nextID(t, svc, service.TierNew, ""); got != 4 {
		t.Fatalf("disabled players must be skipped, got %d", got)
	}
	if _, _, err := svc.MarkReplayAttemptFailed(ctx, 4, errors.New("502")); err != nil {
		t.Fatal(err)
	}
	if got := nextID(t, svc, service.TierNew, ""); got != 0 {
		t.Fatalf("deferred score returned early: %d", got)
	}
	clk.Advance(time.Minute)
	if got := nextID(t, svc, service.TierNew, ""); got != 4 {
		t.Fatalf("deferred score not returned after backoff: %d", got)
	}
}

func TestNextReplaySkipsBusyPlatform(t *testing.T) {
	svc, _, a, _ := seedQueue(t)
	ctx := context.Background()
	busy := service.Busy{{Platform: model.PlatformScoreSaber, Kind: model.KindScore}: true}
	if s, err := svc.NextReplay(ctx, service.TierNew, "", busy); err != nil || s != nil {
		t.Fatalf("busy platform must be skipped, got %+v %v", s, err)
	}
	if err := svc.MarkIdentityError(ctx, a, model.PlatformScoreSaber, "gone", true); err != nil {
		t.Fatal(err)
	}
	if got := nextID(t, svc, service.TierNew, ""); got != 4 {
		t.Fatalf("replays of a disabled account must be skipped, got %d", got)
	}
}

func TestMarkReplayAttemptFailedBackoff(t *testing.T) {
	svc, clk, _, _ := seedQueue(t)
	ctx := context.Background()
	wants := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	for i, want := range wants {
		gaveUp, next, err := svc.MarkReplayAttemptFailed(ctx, 1, errors.New("boom"))
		if err != nil || gaveUp || !next.Equal(clk.Now().Add(want)) {
			t.Fatalf("attempt %d: gaveUp=%v next=%v err=%v", i+1, gaveUp, next, err)
		}
	}
	gaveUp, _, err := svc.MarkReplayAttemptFailed(ctx, 1, errors.New("boom"))
	if err != nil || !gaveUp {
		t.Fatalf("5th attempt must give up (err=%v)", err)
	}
	s, _ := svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayFailed || s.Attempts != service.MaxReplayAttempts || s.LastError != "boom" || s.NextAttemptAt != nil {
		t.Fatalf("score after giving up: %+v", s)
	}
	failed, total, err := svc.ListFailedReplays(ctx, 1, 20)
	if err != nil || total != 1 || failed[0].ID != 1 || failed[0].Leaderboard == nil {
		t.Fatalf("ListFailedReplays = %v %d %v", failed, total, err)
	}
	n, err := svc.RetryFailed(ctx, "", 0)
	if err != nil || n != 1 {
		t.Fatalf("RetryFailed = %d, %v", n, err)
	}
	s, _ = svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayPending || s.Attempts != 0 || s.LastError != "" {
		t.Fatalf("score after retry: %+v", s)
	}
}

func TestMarkReplayGoneAndArchived(t *testing.T) {
	svc, clk, _, _ := seedQueue(t)
	ctx := context.Background()
	if err := svc.MarkReplayGone(ctx, 1, "replay no longer available on ScoreSaber"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkReplayArchived(ctx, 2, 99, "cafe"); err != nil {
		t.Fatal(err)
	}
	s1, _ := svc.GetScore(ctx, 1)
	s2, _ := svc.GetScore(ctx, 2)
	if s1.ReplayState != model.ReplayGone {
		t.Fatalf("score 1 state = %s", s1.ReplayState)
	}
	if s2.ReplayState != model.ReplayArchived || s2.ReplaySize != 99 || s2.ArchivedAt == nil || !s2.ArchivedAt.Equal(clk.Now()) {
		t.Fatalf("score 2 = %+v", s2)
	}
	if err := svc.MarkReplayArchived(ctx, 999, 1, "x"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown score err = %v", err)
	}
}

func TestNextRetryAt(t *testing.T) {
	svc, clk, _, _ := seedQueue(t)
	ctx := context.Background()
	if _, ok, _ := svc.NextRetryAt(ctx); ok {
		t.Fatal("no deferred scores yet")
	}
	_, _, _ = svc.MarkReplayAttemptFailed(ctx, 1, errors.New("x"))
	at, ok, err := svc.NextRetryAt(ctx)
	if err != nil || !ok || !at.Equal(clk.Now().Add(time.Minute)) {
		t.Fatalf("NextRetryAt = %v %v %v", at, ok, err)
	}
}

func TestLinkedAccountOlderPlaysAreBackfill(t *testing.T) {
	svc, _, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	clk.Advance(time.Hour) // the account is linked at T0+1h
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.UpsertFake(t, svc, alice,
		testutil.FakePlay(model.KindScore, "new", "lb-a", testutil.T0.Add(90*time.Minute), true),
		testutil.FakePlay(model.KindScore, "old", "lb-b", testutil.T0.Add(-time.Hour), true))
	sc, err := svc.NextReplay(ctx, service.TierNew, "", nil)
	if err != nil || sc == nil || sc.ExternalID != "new" {
		t.Fatalf("new tier = %+v %v", sc, err)
	}
	_ = svc.MarkReplayGone(ctx, sc.ID, "test")
	if sc, _ := svc.NextReplay(ctx, service.TierNew, "", nil); sc != nil {
		t.Fatalf("plays older than the link are backfill work, got %s", sc.ExternalID)
	}
	if sc, _ := svc.NextReplay(ctx, service.TierBackfill, "", nil); sc == nil || sc.ExternalID != "old" {
		t.Fatalf("backfill tier = %+v", sc)
	}
}
