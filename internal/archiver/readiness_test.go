package archiver_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestBusyPlatformIsSkipped(t *testing.T) {
	fp := testutil.NewFakePlatform()
	fc := newFake()
	svc, _, clk := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil), fp.Platform())
	w := archiver.New(svc)
	ctx := context.Background()
	tess := testutil.AddPlayer(t, svc, "https://tp.example/u/abc") // p00000000001: would be polled first
	testutil.AddPlayer(t, svc, "1001")
	fp.SetPlays(model.KindScore, "abc", testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), true))
	fc.scores["1001"] = fc.history("1001", 1, 1, testutil.T0.Add(-time.Hour))
	fp.Limiter.Block(testutil.T0.Add(30 * time.Second))

	if did, err := w.Step(ctx); err != nil || !did {
		t.Fatalf("step = %v %v", did, err)
	}
	if len(fp.Calls) != 0 {
		t.Fatalf("busy testplat must not be called, calls = %v", fp.Calls)
	}
	if got := fc.calls(); len(got) == 0 || got[0] != "1001:1" {
		t.Fatalf("ScoreSaber must be polled while testplat is busy, calls = %v", got)
	}
	clk.Advance(31 * time.Second)
	for range 20 {
		if did, _ := w.Step(ctx); !did {
			break
		}
	}
	if f := testutil.ScoreFeed(t, svc, tess); f.LastPolledAt == nil {
		t.Fatal("testplat must be polled once its limiter is ready")
	}
}

func TestIdleWakesAtLimiterReadiness(t *testing.T) {
	fp := testutil.NewFakePlatform()
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(newFake(), nil), fp.Platform())
	w := archiver.New(svc)
	ctx := context.Background()
	testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	fp.Limiter.Block(testutil.T0.Add(20 * time.Second))
	if did, _ := w.Step(ctx); did {
		t.Fatal("the only due feed is busy: no work")
	}
	if got := archiver.IdleFor(w, ctx); got != 20*time.Second {
		t.Fatalf("idle = %v, want the 20s until the limiter is ready (not a 1s spin)", got)
	}
}
