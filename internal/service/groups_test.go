package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

const map501 = "hash501/Standard/9" // ScoreSaber leaderboard 501 (HASH501, SoloStandard, Expert+)

// crossFixture: Alice with ScoreSaber and testplat accounts.
//
//	map 501   SS 1 (PB, archived, T0+3m), SS 4 (not PB, T0-1h), TP t1 (950k, T0+4m)
//	map 502   SS 2 (T0+2m)        map 503   SS 3 (no replay, T0+1m)
//	map solo  TP t2 (900, T0+30s) map 504   SS 5 (not PB, T0-2h)
func crossFixture(t *testing.T) (*service.Service, string) {
	t.Helper()
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	s4 := testutil.Item("1001", 4, 501, testutil.T0.Add(-time.Hour), true)
	s4.Score.PersonalBest = false
	s5 := testutil.Item("1001", 5, 504, testutil.T0.Add(-2*time.Hour), true)
	s5.Score.PersonalBest = false
	testutil.Upsert(t, svc, alice,
		testutil.Item("1001", 1, 501, testutil.T0.Add(3*time.Minute), true),
		testutil.Item("1001", 2, 502, testutil.T0.Add(2*time.Minute), true),
		testutil.Item("1001", 3, 503, testutil.T0.Add(time.Minute), false), s4, s5)
	s1, _ := svc.GetScore(ctx, 1)
	testutil.Archive(t, svc, s1, "x")
	t1 := testutil.FakePlay(model.KindScore, "t1", "lb-x501", testutil.T0.Add(4*time.Minute), true)
	t1.Leaderboard.SongHash, t1.Leaderboard.GameMode, t1.Leaderboard.Difficulty = "hash501", "Standard", 9
	t1.ModifiedScore = 950_000
	testutil.UpsertFake(t, svc, alice, t1, testutil.FakePlay(model.KindScore, "t2", "lb-solo", testutil.T0.Add(30*time.Second), false))
	return svc, alice
}

func TestListMapGroupsMergesPlatforms(t *testing.T) {
	svc, alice := crossFixture(t)
	ctx := context.Background()
	list, err := svc.ListMapGroups(ctx, service.ScoreFilter{PlayerID: alice})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 5 || len(list.Groups) != 5 || list.Pages != 1 {
		t.Fatalf("list = total %d, %d groups, %d pages", list.Total, len(list.Groups), list.Pages)
	}
	var order []string
	for _, g := range list.Groups {
		order = append(order, g.Latest.ExternalID)
	}
	if want := []string{"t1", "2", "3", "t2", "5"}; !equal(order, want) {
		t.Fatalf("maps by newest play = %v, want %v", order, want)
	}
	g := list.Groups[0]
	if g.MapKey != map501 || g.Plays != 3 || len(g.Chips) != 2 || g.More() != 1 {
		t.Fatalf("map 501 = %+v", g)
	}
	if g.Chips[0].Platform != model.PlatformScoreSaber || g.Chips[0].ExternalID != "1" || g.Chips[1].ExternalID != "t1" {
		t.Fatalf("one chip per platform, primary first: %s/%s, %s/%s", g.Chips[0].Platform, g.Chips[0].ExternalID, g.Chips[1].Platform, g.Chips[1].ExternalID)
	}
	if g.Latest.Leaderboard == nil || g.Chips[0].Leaderboard == nil {
		t.Fatal("rows come with their leaderboard")
	}
	if last := list.Groups[4]; len(last.Chips) != 0 || last.Latest.ExternalID != "5" || last.More() != 1 {
		t.Fatalf("a map whose only play is not a PB is still listed: %+v", last)
	}
	plays, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: alice, MapKey: map501})
	if err != nil || plays.Total != 3 || plays.Items[0].ExternalID != "t1" || plays.Items[1].ExternalID != "1" || plays.Items[2].ExternalID != "4" {
		t.Fatalf("every play of one map, newest first: %+v %v", plays, err)
	}
	if _, err := svc.ListMapGroups(ctx, service.ScoreFilter{}); err == nil {
		t.Fatal("the merged view is per player")
	}
}

func TestListMapGroupsFiltersAndPaging(t *testing.T) {
	svc, alice := crossFixture(t)
	ctx := context.Background()
	groups := func(f service.ScoreFilter) service.MapGroupList {
		t.Helper()
		f.PlayerID = alice
		l, err := svc.ListMapGroups(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := groups(service.ScoreFilter{PerPage: 3, Page: 2}); l.Total != 5 || l.Pages != 2 || len(l.Groups) != 2 || l.Groups[0].Latest.ExternalID != "t2" {
		t.Fatalf("page 2 = %+v", l)
	}
	tp := groups(service.ScoreFilter{Platform: "testplat"})
	if tp.Total != 2 || len(tp.Groups[0].Chips) != 1 || tp.Groups[0].Chips[0].ExternalID != "t1" {
		t.Fatalf("platform filter = %+v", tp)
	}
	if l := groups(service.ScoreFilter{MinScore: new(int64(960_000))}); l.Total != 4 {
		t.Fatalf("min_score: total %d", l.Total)
	}
	if l := groups(service.ScoreFilter{MaxScore: new(int64(950_000))}); l.Total != 2 {
		t.Fatalf("max_score: total %d", l.Total)
	}
	if l := groups(service.ScoreFilter{Search: "Song 502"}); l.Total != 1 {
		t.Fatalf("search: total %d", l.Total)
	}
	if l := groups(service.ScoreFilter{State: service.FilterArchived}); l.Total != 1 || l.Groups[0].MapKey != map501 {
		t.Fatalf("state: %+v", l)
	}
	if l := groups(service.ScoreFilter{Platform: model.PlatformScoreSaber, MaxScore: new(int64(950_000))}); l.Total != 0 || len(l.Groups) != 0 || l.Pages != 1 {
		t.Fatalf("combined filters: %+v", l)
	}
}

func TestListScoresPlatformAndScoreBounds(t *testing.T) {
	svc, alice := crossFixture(t)
	ctx := context.Background()
	count := func(f service.ScoreFilter) int64 {
		t.Helper()
		f.PlayerID = alice
		l, err := svc.ListScores(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return l.Total
	}
	for name, c := range map[string]struct {
		f    service.ScoreFilter
		want int64
	}{
		"all":      {service.ScoreFilter{}, 7},
		"platform": {service.ScoreFilter{Platform: "testplat"}, 2},
		"min":      {service.ScoreFilter{MinScore: new(int64(960_000))}, 5},
		"max":      {service.ScoreFilter{MaxScore: new(int64(900))}, 1},
		"between":  {service.ScoreFilter{MinScore: new(int64(900)), MaxScore: new(int64(950_000))}, 2},
		"combined": {service.ScoreFilter{Platform: model.PlatformScoreSaber, MaxScore: new(int64(950_000))}, 0},
		"with map": {service.ScoreFilter{MapKey: map501, Platform: model.PlatformScoreSaber}, 2},
	} {
		if got := count(c.f); got != c.want {
			t.Errorf("%s: total %d, want %d", name, got, c.want)
		}
	}
}

func TestGetPlay(t *testing.T) {
	svc, alice := crossFixture(t)
	ctx := context.Background()
	sc, err := svc.GetPlay(ctx, "testplat", model.KindScore, "t1")
	if err != nil || sc.PlayerID != alice || sc.Leaderboard == nil || sc.Player == nil || sc.Player.Name != "Alice" {
		t.Fatalf("GetPlay = %+v %v", sc, err)
	}
	if sc, err := svc.GetPlay(ctx, model.PlatformScoreSaber, model.KindScore, "1"); err != nil || sc.ID != 1 {
		t.Fatalf("legacy row = %+v %v", sc, err)
	}
	for _, c := range [][3]string{{"testplat", model.KindAttempt, "t1"}, {"testplat", model.KindScore, "1"}, {"nope", model.KindScore, "t1"}} {
		if _, err := svc.GetPlay(ctx, c[0], c[1], c[2]); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("GetPlay%v = %v", c, err)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
