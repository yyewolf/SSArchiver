package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// typesFixture: Tess (testplat) with one score and one attempt of each end
// type except unknown, each on its own map; the fail shares the score's map.
func typesFixture(t *testing.T) (*service.Service, string) {
	t.Helper()
	svc, _, _ := testutil.NewMultiService(t)
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	plays := []platform.Play{testutil.FakePlay(model.KindScore, "s1", "lb-s", testutil.T0, true)}
	for i, end := range []string{model.EndClear, model.EndFail, model.EndQuit, model.EndRestart, model.EndPractice} {
		lb := "lb-" + end
		if end == model.EndFail {
			lb = "lb-s"
		}
		p := testutil.FakePlay(model.KindAttempt, "a-"+end, lb, testutil.T0.Add(-time.Duration(i+1)*time.Minute), true)
		p.EndType = end
		plays = append(plays, p)
	}
	testutil.UpsertFake(t, svc, tess, plays...)
	return svc, tess
}

func TestTypeFilter(t *testing.T) {
	svc, tess := typesFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		types []string
		want  int64
	}{
		{nil, 2}, // the score and the clear attempt
		{[]string{service.TypeComplete}, 2},
		{[]string{model.EndFail}, 1},
		{[]string{model.EndFail, model.EndQuit}, 2},
		{[]string{service.TypeComplete, model.EndPractice}, 3},
		{[]string{service.TypeAll}, 6},
		{[]string{model.EndRestart, service.TypeAll}, 6},
	} {
		l, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: tess, Types: c.types})
		if err != nil || l.Total != c.want {
			t.Errorf("types %v: total %d, want %d (%v)", c.types, l.Total, c.want, err)
		}
	}
	groups, _ := svc.ListMapGroups(ctx, service.ScoreFilter{PlayerID: tess})
	if groups.Total != 2 { // lb-s (score) and lb-clear
		t.Fatalf("by default the merged view hides maps that only have non-clear attempts: %d", groups.Total)
	}
	fails, _ := svc.ListMapGroups(ctx, service.ScoreFilter{PlayerID: tess, Types: []string{model.EndFail}})
	if fails.Total != 1 || len(fails.Groups[0].Chips) != 1 || fails.Groups[0].Latest.ExternalID != "a-fail" {
		t.Fatalf("type=fail lists the map with its PB chip and the fail as newest play: %+v", fails.Groups)
	}
}

func TestParseTypes(t *testing.T) {
	if got, err := service.ParseTypes(""); err != nil || got != nil {
		t.Fatalf("empty = %v %v", got, err)
	}
	if got, err := service.ParseTypes(" fail, quit,fail "); err != nil || len(got) != 2 || got[0] != "fail" || got[1] != "quit" {
		t.Fatalf("list = %v %v", got, err)
	}
	if _, err := service.ParseTypes("fail,bogus"); !errors.Is(err, service.ErrInvalidFilter) {
		t.Fatalf("unknown type = %v", err)
	}
}

func TestDefaultViewsHideAttempts(t *testing.T) {
	svc, tess := typesFixture(t)
	ctx := context.Background()
	sum, _ := svc.GetPlayerSummary(ctx, tess)
	if sum.Counts.Scores != 1 || sum.Identities[0].Counts[model.KindAttempt].Scores != 5 {
		t.Fatalf("headline counts are score rows; attempts are counted per feed: %+v / %+v", sum.Counts, sum.Identities[0].Counts)
	}
	if row := testutil.Row(t, svc, tess, "a-quit"); row.Kind != model.KindAttempt {
		t.Fatal("testutil.Row finds rows of every type")
	}
}
