package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestParsePlayerRef(t *testing.T) {
	ok := map[string]string{
		"76561198059961776":                                             "76561198059961776",
		"  76561198059961776 \n":                                        "76561198059961776",
		"https://scoresaber.com/u/76561198059961776":                    "76561198059961776",
		"https://scoresaber.com/u/76561198059961776?page=2&sort=recent": "76561198059961776",
		"scoresaber.com/u/1922350521131465/":                            "1922350521131465",
		"http://www.scoresaber.com/u/1922350521131465#top":              "1922350521131465",
	}
	for in, want := range ok {
		got, err := service.ParsePlayerRef(in)
		if err != nil || got != want {
			t.Errorf("ParsePlayerRef(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "../123", "https://evil.com/u/123", "https://scoresaber.com/u/12a", "https://scoresaber.com/leaderboard/123"} {
		if _, err := service.ParsePlayerRef(in); !errors.Is(err, service.ErrInvalidPlayerRef) {
			t.Errorf("ParsePlayerRef(%q) err = %v, want ErrInvalidPlayerRef", in, err)
		}
	}
}

func TestAddPlayer(t *testing.T) {
	svc, res, _ := testutil.NewService(t)
	ctx := context.Background()
	p, err := svc.AddPlayer(ctx, "https://scoresaber.com/u/1001")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Alice" || !p.Enabled || p.BackfillState != model.BackfillPending || p.BackfillPage != 1 || !p.AddedAt.Equal(testutil.T0) {
		t.Fatalf("unexpected player: %+v", p)
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("AddPlayer must wake the worker")
	}
	if _, err := svc.AddPlayer(ctx, "1001"); !errors.Is(err, service.ErrPlayerExists) {
		t.Fatalf("duplicate add err = %v", err)
	}
	if _, err := svc.AddPlayer(ctx, "9999"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown player err = %v", err)
	}
	calls := res.Calls
	if _, err := svc.AddPlayer(ctx, "not a player"); !errors.Is(err, service.ErrInvalidPlayerRef) || res.Calls != calls {
		t.Fatalf("invalid ref must fail without calling ScoreSaber (err=%v)", err)
	}
}

func TestListPlayersWithCounts(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	items := []scoreItem{{1, true}, {2, true}, {3, false}}
	upsert(t, svc, "1001", clk, items)
	if err := svc.MarkReplayArchived(ctx, 1, 10, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPlayerEnabled(ctx, "1002", false); err != nil {
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
	mustAdd(t, svc, "1001")
	upsert(t, svc, "1001", clk, []scoreItem{{1, true}})
	if _, _, err := svc.Store().Put("1001", 1, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeletePlayer(ctx, "1001", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetPlayer(ctx, "1001"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("player still exists: %v", err)
	}
	if _, err := svc.GetScore(ctx, 1); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("score still exists: %v", err)
	}
	if _, err := svc.Store().Open("1001", 1); err == nil {
		t.Fatal("replay file still exists")
	}
	if err := svc.DeletePlayer(ctx, "1001", false); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestRequestPollWakesAndClearsLastPolled(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	<-svc.WakeC()
	if err := svc.MarkPolled(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPoll(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	p, _ := svc.GetPlayer(ctx, "1001")
	if p.LastPolledAt != nil {
		t.Fatal("RequestPoll must clear last_polled_at")
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("RequestPoll must wake the worker")
	}
}
