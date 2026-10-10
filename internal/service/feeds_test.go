package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestDueFeed(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	f, err := svc.DueFeed(ctx, 10*time.Minute, nil)
	if err != nil || f == nil {
		t.Fatalf("never-polled feed must be due: %v %v", f, err)
	}
	if f.ExternalID != "1001" || f.PlayerName != "Alice" || f.Feed != model.KindScore || f.Platform != model.PlatformScoreSaber {
		t.Fatalf("work feed = %+v", f)
	}
	_ = svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey("1001"))
	clk.Advance(time.Minute)
	_ = svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey("1002"))
	if f, _ := svc.DueFeed(ctx, 10*time.Minute, nil); f != nil {
		t.Fatalf("nothing should be due, got %+v", f.Key())
	}
	clk.Advance(10 * time.Minute)
	if f, _ := svc.DueFeed(ctx, 10*time.Minute, nil); f == nil || f.PlayerID != "1001" {
		t.Fatalf("oldest poll first, got %+v", f)
	}
	busy := service.Busy{{Platform: model.PlatformScoreSaber, Kind: model.KindScore}: true}
	if f, _ := svc.DueFeed(ctx, 10*time.Minute, busy); f != nil {
		t.Fatalf("busy platforms are skipped, got %+v", f.Key())
	}
	_ = svc.SetPlayerEnabled(ctx, "1001", false)
	if f, _ := svc.DueFeed(ctx, 10*time.Minute, nil); f == nil || f.PlayerID != "1002" {
		t.Fatalf("disabled players are never due, got %+v", f)
	}
	at, ok, err := svc.NextPollAt(ctx, 10*time.Minute)
	if err != nil || !ok || !at.Equal(testutil.T0.Add(11*time.Minute)) {
		t.Fatalf("NextPollAt = %v %v %v", at, ok, err)
	}
}

func TestMarkIdentityErrorDisablesOnlyTheAccount(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	if err := svc.MarkIdentityError(ctx, "1001", model.PlatformScoreSaber, "player not found on ScoreSaber", true); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.GetPlayerSummary(ctx, "1001")
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Enabled || sum.Identities[0].Enabled || sum.Error() != "player not found on ScoreSaber" {
		t.Fatalf("summary = %+v", sum)
	}
	if f, _ := svc.DueFeed(ctx, time.Minute, nil); f != nil {
		t.Fatal("feeds of a disabled account must not be due")
	}
	if err := svc.SetPlayerEnabled(ctx, "1001", true); err != nil {
		t.Fatal(err)
	}
	sum, _ = svc.GetPlayerSummary(ctx, "1001")
	if !sum.Identities[0].Enabled || sum.Error() != "" {
		t.Fatalf("enabling the player must re-enable its accounts: %+v", sum.Identities[0])
	}
}

func TestUpdatePlayerProfileKeepsEmptyFields(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	_ = svc.UpdatePlayerProfile(ctx, "1001", scoresaber.Player{Name: "Alice2", Avatar: "a.jpg"})
	p, _ := svc.GetPlayer(ctx, "1001")
	if p.Name != "Alice2" || p.AvatarURL != "a.jpg" || p.Country != "FR" {
		t.Fatalf("profile update = %+v (empty fields must not overwrite)", p)
	}
}

func TestFeedErrorsClearOnPoll(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	k := testutil.ScoreFeedKey("1001")
	_ = svc.MarkFeedError(ctx, k, "502")
	if got := testutil.ScoreFeed(t, svc, "1001").LastError; got != "502" {
		t.Fatalf("last_error = %q", got)
	}
	_ = svc.MarkFeedPolled(ctx, k)
	if f := testutil.ScoreFeed(t, svc, "1001"); f.LastError != "" || f.LastPolledAt == nil {
		t.Fatalf("MarkFeedPolled must set last_polled_at and clear last_error: %+v", f)
	}
}

func TestNextBackfillFeed(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	k1, k2 := testutil.ScoreFeedKey("1001"), testutil.ScoreFeedKey("1002")
	if f, _ := svc.NextBackfillFeed(ctx, "", true, nil); f != nil {
		t.Fatal("feeds must be polled once before backfilling")
	}
	_ = svc.MarkFeedPolled(ctx, k1)
	_ = svc.MarkFeedPolled(ctx, k2)
	if f, _ := svc.NextBackfillFeed(ctx, "", true, nil); f == nil || f.PlayerID != "1001" {
		t.Fatalf("got %+v", f)
	}
	if f, _ := svc.NextBackfillFeed(ctx, k1.String(), true, nil); f == nil || f.PlayerID != "1002" {
		t.Fatalf("round robin: got %+v", f)
	}
	if f, _ := svc.NextBackfillFeed(ctx, "", false, nil); f != nil {
		t.Fatalf("no non-score feeds exist, got %+v", f.Key())
	}
	if err := svc.SetFeedBackfill(ctx, k2, model.BackfillDone, 85, 84); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.NextBackfillFeed(ctx, k1.String(), true, nil); f == nil || f.PlayerID != "1001" {
		t.Fatalf("done feeds are skipped: got %+v", f)
	}
	if err := svc.DeferFeedBackfill(ctx, k1, clk.Now().Add(5*time.Minute), "502"); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.NextBackfillFeed(ctx, "", true, nil); f != nil {
		t.Fatalf("deferred feed returned early: %+v", f.Key())
	}
	clk.Advance(5 * time.Minute)
	f, _ := svc.NextBackfillFeed(ctx, "", true, nil)
	if f == nil || f.PlayerID != "1001" || f.LastError != "502" {
		t.Fatalf("deferred feed after delay: %+v", f)
	}
	_ = svc.SetFeedBackfill(ctx, k1, model.BackfillRunning, 3, 84)
	got := testutil.ScoreFeed(t, svc, "1001")
	if got.BackfillPage != 3 || got.BackfillTotalPages != 84 || got.BackfillRetryAt != nil {
		t.Fatalf("SetFeedBackfill = %+v", got)
	}
	if err := svc.SetFeedBackfill(ctx, testutil.ScoreFeedKey("nobody"), model.BackfillDone, 1, 1); err == nil {
		t.Fatal("unknown feeds must report ErrNotFound")
	}
}

func TestPlayerSummary(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	_ = svc.SetFeedBackfill(ctx, testutil.ScoreFeedKey("1001"), model.BackfillRunning, 4, 9)
	sum, err := svc.GetPlayerSummary(ctx, "1001")
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Identities) != 1 || sum.Identities[0].ExternalID != "1001" || len(sum.Identities[0].Feeds) != 1 {
		t.Fatalf("identities = %+v", sum.Identities)
	}
	if s := sum.Sync(); s.BackfillState != model.BackfillRunning || s.BackfillPage != 4 || s.BackfillTotalPages != 9 {
		t.Fatalf("Sync() = %+v", s)
	}
	list, err := svc.ListPlayers(ctx, true)
	if err != nil || len(list) != 1 || list[0].Sync().BackfillPage != 4 {
		t.Fatalf("ListPlayers = %+v %v", list, err)
	}
	if _, err := svc.GetPlayerSummary(ctx, "nobody"); err == nil {
		t.Fatal("unknown player must be ErrNotFound")
	}
}
