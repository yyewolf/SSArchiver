package archiver_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// TestThirdPlatformEndToEnd is spec §9's genericity test: a platform that
// only exists in tests must work through link, poll, backfill, replay storage
// and reconciliation with no code outside its adapter.
func TestThirdPlatformEndToEnd(t *testing.T) {
	fp := testutil.NewFakePlatform()
	fc := newFake()
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil), fp.Platform())
	w := archiver.New(svc)
	ctx := context.Background()

	tess, err := svc.AddPlayer(ctx, "https://tp.example/u/abc", "") // URL picks the platform
	if err != nil {
		t.Fatal(err)
	}
	if tess.Name != "Tess" {
		t.Fatalf("profile not resolved: %+v", tess)
	}
	alice := testutil.AddPlayer(t, svc, "1001")
	fc.scores["1001"] = fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	var plays []platform.Play
	for i := range 5 { // 3 pages at PerPage 2
		plays = append(plays, testutil.FakePlay(model.KindScore, "s"+string(rune('a'+i)), "lb-"+string(rune('a'+i%2)), testutil.T0.Add(-time.Duration(i)*time.Minute), true))
	}
	fp.SetPlays(model.KindScore, "abc", plays...)

	for range 100 {
		did, err := w.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			break
		}
	}

	c, _ := svc.PlayerCounts(ctx, tess.ID)
	if c.Scores != 5 || c.Archived != 5 {
		t.Fatalf("testplat counts = %+v", c)
	}
	if c, _ := svc.PlayerCounts(ctx, alice); c.Archived != 2 {
		t.Fatalf("the legacy platform must keep working alongside: %+v", c)
	}
	list, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: tess.ID, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range list.Items {
		if sc.ID < platform.InternalIDBase || sc.Platform != "testplat" {
			t.Fatalf("row = %+v", sc)
		}
		path, err := svc.ReplayPath(sc)
		if err != nil {
			t.Fatal(err)
		}
		want := svc.Store().Path(storageLoc(tess.ID, "testplat", sc.ID, ".tpr"))
		if path != want {
			t.Fatalf("replay path = %s, want %s", path, want)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != "tp-replay-"+sc.ExternalID {
			t.Fatalf("replay file %s: %q %v", path, b, err)
		}
	}
	if f := testutil.ScoreFeed(t, svc, tess.ID); f.BackfillState != model.BackfillDone {
		t.Fatalf("testplat backfill = %+v", f)
	}

	// Reconciliation understands the per-platform layout.
	victim := list.Items[0]
	path, _ := svc.ReplayPath(victim)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ReconcileStorage(ctx)
	if err != nil || res.Requeued != 1 || res.Orphans != 0 {
		t.Fatalf("reconcile = %+v %v", res, err)
	}
}

func storageLoc(player, dir string, row int64, ext string) storage.Loc {
	return storage.Loc{PlayerID: player, Dir: dir, RowID: row, Ext: ext}
}
