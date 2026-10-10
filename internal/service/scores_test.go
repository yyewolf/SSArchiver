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

func TestUpsertScoresStates(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	mustAdd(t, svc, "1001")
	res := upsert(t, svc, "1001", clk, []scoreItem{{1, true}, {2, false}})
	if res.New != 2 || res.Known != 0 || res.NewReplays != 1 {
		t.Fatalf("result = %+v", res)
	}
	s1, _ := svc.GetScore(context.Background(), 1)
	s2, _ := svc.GetScore(context.Background(), 2)
	if s1.ReplayState != model.ReplayPending || s2.ReplayState != model.ReplayNone {
		t.Fatalf("states = %s, %s", s1.ReplayState, s2.ReplayState)
	}
	if s1.Leaderboard == nil || s1.Leaderboard.SongName != "Song 1001" || s1.Player == nil || s1.Player.Name != "Alice" {
		t.Fatalf("GetScore must preload leaderboard and player: %+v", s1)
	}
}

func TestUpsertScoresPreservesArchiveState(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	item := testutil.Item("1001", 1, 1001, testutil.T0, true)
	if _, err := svc.UpsertScores(ctx, "1001", []scoresaber.ScoreItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkReplayArchived(ctx, 1, 1234, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	item.Score.Rank = 7
	item.Score.PersonalBest = false
	res, err := svc.UpsertScores(ctx, "1001", []scoresaber.ScoreItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if res.Known != 1 || res.New != 0 || res.NewReplays != 0 {
		t.Fatalf("result = %+v", res)
	}
	s, _ := svc.GetScore(ctx, 1)
	if s.Rank != 7 || s.PersonalBest {
		t.Fatalf("mutable fields not updated: rank=%d pb=%v", s.Rank, s.PersonalBest)
	}
	if s.ReplayState != model.ReplayArchived || s.ReplaySHA256 != "deadbeef" || s.ReplaySize != 1234 || s.ArchivedAt == nil {
		t.Fatalf("archive state clobbered: %+v", s)
	}
}

func TestUpsertScoresReplayAppearsLater(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	item := testutil.Item("1001", 1, 1001, testutil.T0, false)
	_, _ = svc.UpsertScores(ctx, "1001", []scoresaber.ScoreItem{item})
	item.Score.HasReplay = true
	res, _ := svc.UpsertScores(ctx, "1001", []scoresaber.ScoreItem{item})
	s, _ := svc.GetScore(ctx, 1)
	if res.NewReplays != 1 || s.ReplayState != model.ReplayPending || !s.HasReplay {
		t.Fatalf("res=%+v state=%s", res, s.ReplayState)
	}
}

func TestListScoresFiltersAndPaging(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	var items []scoresaber.ScoreItem
	for i := int64(1); i <= 5; i++ {
		it := testutil.Item("1001", i, 1000+i, testutil.T0.Add(time.Duration(i)*time.Minute), i%2 == 1)
		if i == 2 {
			it.Leaderboard.Map.SongName = "Ghost Rule"
			it.Leaderboard.Realm.LeaderboardStatus = "RANKED"
		}
		if i == 4 {
			it.Leaderboard.Map.LevelAuthorName = "GhostMapper"
		}
		items = append(items, it)
	}
	if _, err := svc.UpsertScores(ctx, "1001", items); err != nil {
		t.Fatal(err)
	}
	_ = svc.MarkReplayArchived(ctx, 5, 1, "x")

	all, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", PerPage: 2, Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 5 || all.Pages != 3 || len(all.Items) != 2 || all.Items[0].ID != 5 || all.Items[0].Leaderboard == nil {
		t.Fatalf("page 1 = %+v", all)
	}
	p3, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", PerPage: 2, Page: 3})
	if len(p3.Items) != 1 || p3.Items[0].ID != 1 {
		t.Fatalf("page 3 = %+v", p3.Items)
	}
	ids := func(l service.ScoreList) []int64 {
		var out []int64
		for _, s := range l.Items {
			out = append(out, s.ID)
		}
		return out
	}
	search, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", Search: "ghost"})
	if got := ids(search); len(got) != 2 || got[0] != 4 || got[1] != 2 {
		t.Fatalf("search ids = %v, want [4 2] (song name + mapper match)", got)
	}
	ranked, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", RankedOnly: true})
	if got := ids(ranked); len(got) != 1 || got[0] != 2 {
		t.Fatalf("ranked ids = %v", got)
	}
	withReplay, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", State: service.FilterWithReplay})
	if withReplay.Total != 3 {
		t.Fatalf("with replay total = %d", withReplay.Total)
	}
	archived, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", State: service.FilterArchived})
	if got := ids(archived); len(got) != 1 || got[0] != 5 {
		t.Fatalf("archived ids = %v", got)
	}
	wild, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", Search: "%"})
	if wild.Total != 5 {
		t.Fatalf("a bare %% must not act as a wildcard filter that drops rows: total = %d", wild.Total)
	}
}

func TestUpsertSetsPlatformColumns(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	upsert(t, svc, "1001", clk, []scoreItem{{id: 7, hasReplay: true}})
	sc, err := svc.GetScore(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Platform != model.PlatformScoreSaber || sc.Kind != model.KindScore || sc.EndType != model.EndClear || sc.ExternalID != "7" || sc.ReplayURL != nil {
		t.Fatalf("score = %+v", sc)
	}
	lb := sc.Leaderboard
	if lb.Platform != model.PlatformScoreSaber || lb.ExternalID != "1007" || lb.MapKey != "hash1007/Standard/9" {
		t.Fatalf("leaderboard = %+v", lb)
	}
}
