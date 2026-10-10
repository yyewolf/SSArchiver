package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestAddPlayer(t *testing.T) {
	svc, res, _ := testutil.NewService(t)
	ctx := context.Background()
	p, err := svc.AddPlayer(ctx, "https://scoresaber.com/u/1001", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Alice" || !p.Enabled || !p.AddedAt.Equal(testutil.T0) {
		t.Fatalf("unexpected player: %+v", p)
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("AddPlayer must wake the worker")
	}
	if _, err := svc.AddPlayer(ctx, "1001", ""); !errors.Is(err, service.ErrPlayerExists) {
		t.Fatalf("duplicate add err = %v", err)
	}
	if _, err := svc.AddPlayer(ctx, "9999", ""); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown player err = %v", err)
	}
	calls := res.Calls
	if _, err := svc.AddPlayer(ctx, "not a player", ""); !errors.Is(err, service.ErrInvalidPlayerRef) || res.Calls != calls {
		t.Fatalf("invalid ref must fail without calling ScoreSaber (err=%v)", err)
	}
}

func TestListPlayersWithCounts(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	a := mustAdd(t, svc, "1001")
	b := mustAdd(t, svc, "1002")
	items := []scoreItem{{1, true}, {2, true}, {3, false}}
	upsert(t, svc, a, clk, items)
	if err := svc.MarkReplayArchived(ctx, 1, 10, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPlayerEnabled(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	enabled, err := svc.ListPlayers(ctx, false)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("enabled players = %d (%v)", len(enabled), err)
	}
	c := enabled[0].Counts
	if c.Scores != 3 || c.Archived != 1 || c.Pending != 1 || c.Replays() != 2 {
		t.Fatalf("counts = %+v", c)
	}
	all, _ := svc.ListPlayers(ctx, true)
	if len(all) != 2 || all[0].Name != "Alice" || all[1].Name != "Bob" {
		t.Fatalf("all players = %+v", all)
	}
}

func TestDeletePlayer(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	a := mustAdd(t, svc, "1001")
	upsert(t, svc, a, clk, []scoreItem{{1, true}})
	if _, _, err := svc.Store().Put(storage.Loc{PlayerID: a, RowID: 1, Ext: ".dat"}, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeletePlayer(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetPlayer(ctx, a); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("player still exists: %v", err)
	}
	if _, err := svc.GetScore(ctx, 1); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("score still exists: %v", err)
	}
	if _, err := svc.Store().Open(storage.Loc{PlayerID: a, RowID: 1, Ext: ".dat"}); err == nil {
		t.Fatal("replay file still exists")
	}
	if err := svc.DeletePlayer(ctx, a, false); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestRequestPollWakesAndClearsLastPolled(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	a := mustAdd(t, svc, "1001")
	<-svc.WakeC()
	if err := svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey(a)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPoll(ctx, a); err != nil {
		t.Fatal(err)
	}
	if f := testutil.ScoreFeed(t, svc, a); f.LastPolledAt != nil {
		t.Fatal("RequestPoll must clear last_polled_at")
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("RequestPoll must wake the worker")
	}
}

func TestAddPlayerCreatesAccountAndScoreFeed(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	p, err := svc.AddPlayer(ctx, "1001", "")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Identities(ctx, p.ID)
	if err != nil || len(ids) != 1 {
		t.Fatalf("identities = %v %v", ids, err)
	}
	if ids[0].Platform != model.PlatformScoreSaber || ids[0].ExternalID != "1001" || !ids[0].Enabled || !ids[0].LinkedAt.Equal(testutil.T0) {
		t.Fatalf("identity = %+v", ids[0])
	}
	feeds, err := svc.Feeds(ctx, p.ID)
	if err != nil || len(feeds) != 1 {
		t.Fatalf("feeds = %v %v", feeds, err)
	}
	f := feeds[0]
	if f.Feed != model.KindScore || !f.Enabled || f.Access != model.AccessNA || f.BackfillState != model.BackfillPending ||
		f.BackfillPage != 1 || f.LastPolledAt != nil || !f.StartedAt.Equal(testutil.T0) {
		t.Fatalf("feed = %+v", f)
	}
}

func TestAddPlayerAssignsOpaqueID(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	svc.SetIDGenerator(platform.NewPlayerID)
	p, err := svc.AddPlayer(ctx, "https://scoresaber.com/u/1001", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "1001" || !platform.ValidPlayerID(p.ID) {
		t.Fatalf("player ID = %q, want an opaque ID", p.ID)
	}
	got, err := svc.PlayerByIdentity(ctx, model.PlatformScoreSaber, "1001")
	if err != nil || got.ID != p.ID {
		t.Fatalf("PlayerByIdentity = %+v %v", got, err)
	}
	if _, err := svc.PlayerByIdentity(ctx, model.PlatformScoreSaber, "1002"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("untracked account err = %v", err)
	}
}

func TestAddPlayerTwiceIsRejected(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	testutil.AddPlayer(t, svc, "1001")
	for _, ref := range []string{"1001", " https://scoresaber.com/u/1001?page=2 ", "scoresaber.com/u/1001/"} {
		if _, err := svc.AddPlayer(ctx, ref, ""); !errors.Is(err, service.ErrPlayerExists) {
			t.Errorf("AddPlayer(%q) err = %v, want ErrPlayerExists", ref, err)
		}
	}
	list, _ := svc.ListPlayers(ctx, true)
	if len(list) != 1 {
		t.Fatalf("players = %d, want 1", len(list))
	}
}

func TestIDGeneratorCollisionRetries(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	first := testutil.AddPlayer(t, svc, "1001")
	ids := []string{first, first, "pfresh"}
	svc.SetIDGenerator(func() string { id := ids[0]; ids = ids[1:]; return id })
	p, err := svc.AddPlayer(ctx, "1002", "")
	if err != nil || p.ID != "pfresh" {
		t.Fatalf("collision not retried: %+v %v", p, err)
	}
}
