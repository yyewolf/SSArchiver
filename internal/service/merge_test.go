package service_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// mergeFixture is Alice (ScoreSaber, score 1 archived) and Tess (testplat:
// t1 archived, t2 pending, backfill cursor at page 3).
func mergeFixture(t *testing.T) (svc *service.Service, alice, tess string, t1 *model.Score) {
	t.Helper()
	svc, _, _ = testutil.NewMultiService(t)
	ctx := context.Background()
	alice = mustAdd(t, svc, "1001")
	tess = mustAdd(t, svc, "https://tp.example/u/abc")
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true))
	s1, _ := svc.GetScore(ctx, 1)
	testutil.Archive(t, svc, s1, "alice replay")
	testutil.UpsertFake(t, svc, tess,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0, true))
	t1 = testutil.Row(t, svc, tess, "t1")
	testutil.Archive(t, svc, t1, "tess replay")
	if err := svc.SetFeedBackfill(ctx, service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindScore}, model.BackfillRunning, 3, 9); err != nil {
		t.Fatal(err)
	}
	return svc, alice, tess, t1
}

func readReplay(t *testing.T, svc *service.Service, sc *model.Score) string {
	t.Helper()
	f, err := svc.OpenReplay(sc)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}

func TestMergeMovesEverything(t *testing.T) {
	svc, alice, tess, t1 := mergeFixture(t)
	ctx := context.Background()
	oldPath, _ := svc.ReplayPath(t1)

	if err := svc.MergePlayers(ctx, tess, alice); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.GetPlayer(ctx, tess); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("the source player must be gone")
	}
	if cur, aliased, err := svc.ResolvePlayerID(ctx, tess); err != nil || !aliased || cur != alice {
		t.Fatalf("alias = %s %v %v", cur, aliased, err)
	}
	if cur, aliased, _ := svc.ResolvePlayerID(ctx, alice); cur != alice || aliased {
		t.Fatal("a live player resolves to itself")
	}
	if _, _, err := svc.ResolvePlayerID(ctx, "nobody"); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("unknown IDs are not found")
	}
	sum, _ := svc.GetPlayerSummary(ctx, alice)
	if len(sum.Identities) != 2 || sum.Counts.Scores != 3 || sum.Counts.Archived != 2 || sum.Name != "Alice" {
		t.Fatalf("survivor = %+v %+v", sum.Player, sum.Counts)
	}
	if f := sum.Identities[1].Feed(model.KindScore); f.BackfillState != model.BackfillRunning || f.BackfillPage != 3 {
		t.Fatalf("the moved feed keeps its cursor: %+v", f)
	}
	moved := testutil.Row(t, svc, alice, "t1")
	if moved.ID != t1.ID || moved.ReplayState != model.ReplayArchived || readReplay(t, svc, moved) != "tess replay" {
		t.Fatalf("moved row = %+v", moved)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the source directory is removed last")
	}
	if p, _ := svc.PlayerByIdentity(ctx, "testplat", "abc"); p == nil || p.ID != alice {
		t.Fatal("the account now belongs to the survivor")
	}
	evs, _, _ := svc.ListEvents(ctx, service.EventFilter{PlayerID: alice, PerPage: 50})
	var msgs []string
	for _, e := range evs {
		msgs = append(msgs, e.Message)
	}
	if all := strings.Join(msgs, "\n"); !strings.Contains(all, "player added: Tess") || !strings.Contains(all, "merged Tess into Alice") {
		t.Fatalf("events follow the survivor:\n%s", all)
	}
	if res, err := svc.ReconcileStorage(ctx); err != nil || res.Requeued != 0 || res.Orphans != 0 {
		t.Fatalf("storage after merge: %+v %v", res, err)
	}
}

func TestMergeAdoptsPrimaryProfile(t *testing.T) {
	svc, alice, tess, _ := mergeFixture(t)
	ctx := context.Background()
	if err := svc.MergePlayers(ctx, alice, tess); err != nil {
		t.Fatal(err)
	}
	p, _ := svc.GetPlayer(ctx, tess)
	if p.Name != "Alice" || p.Country != "FR" {
		t.Fatalf("the survivor shows its primary (ScoreSaber) profile: %+v", p)
	}
	sum, _ := svc.GetPlayerSummary(ctx, tess)
	if sum.Identities[0].Platform != model.PlatformScoreSaber {
		t.Fatal("ScoreSaber is the primary account")
	}
}

func TestMergeRefusals(t *testing.T) {
	svc, alice, tess, _ := mergeFixture(t)
	ctx := context.Background()
	bob := mustAdd(t, svc, "1002")
	if err := svc.MergePlayers(ctx, bob, alice); !errors.Is(err, service.ErrMergeConflict) {
		t.Fatalf("two ScoreSaber players: %v", err)
	}
	if err := svc.MergePlayers(ctx, alice, alice); !errors.Is(err, service.ErrMergeSelf) {
		t.Fatalf("self: %v", err)
	}
	if err := svc.MergePlayers(ctx, "nobody", alice); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown source: %v", err)
	}
	if err := svc.MergePlayers(ctx, tess, "nobody"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown target: %v", err)
	}
	for _, id := range []string{alice, bob, tess} {
		if _, err := svc.GetPlayer(ctx, id); err != nil {
			t.Fatalf("refusals change nothing: %s %v", id, err)
		}
	}
}

func TestMergeRepointsAliases(t *testing.T) {
	tp, zp := testutil.NewFakePlatform(), testutil.NewFakePlatformAs("zedplat", "zp", "ZedPlat")
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(&testutil.Resolver{Players: testutil.DefaultPlayers()}, nil), tp.Platform(), zp.Platform())
	ctx := context.Background()
	a := mustAdd(t, svc, "1001")
	x := mustAdd(t, svc, "https://tp.example/u/abc")
	z := mustAdd(t, svc, "https://zp.example/u/abc")
	if err := svc.MergePlayers(ctx, x, a); err != nil {
		t.Fatal(err)
	}
	if err := svc.MergePlayers(ctx, a, z); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{x, a} {
		if cur, aliased, err := svc.ResolvePlayerID(ctx, old); err != nil || !aliased || cur != z {
			t.Fatalf("%s resolves to %s (%v %v), want %s: aliases never chain", old, cur, aliased, err, z)
		}
	}
	if sum, _ := svc.GetPlayerSummary(ctx, z); len(sum.Identities) != 3 {
		t.Fatalf("identities = %d", len(sum.Identities))
	}
}

func TestMergeRerunAfterFileLinkStep(t *testing.T) {
	svc, alice, tess, t1 := mergeFixture(t)
	ctx := context.Background()
	// A previous merge crashed right after step 1: the file is already linked.
	from := storage.Loc{PlayerID: tess, Dir: "testplat", RowID: t1.ID, Ext: ".tpr"}
	to := storage.Loc{PlayerID: alice, Dir: "testplat", RowID: t1.ID, Ext: ".tpr"}
	if err := svc.Store().Link(from, to); err != nil {
		t.Fatal(err)
	}
	if res, _ := svc.ReconcileStorage(ctx); res.Orphans != 1 {
		t.Fatalf("the half-merged copy is an orphan until the rows move: %+v", res)
	}
	if err := svc.MergePlayers(ctx, tess, alice); err != nil {
		t.Fatal(err)
	}
	if got := readReplay(t, svc, testutil.Row(t, svc, alice, "t1")); got != "tess replay" {
		t.Fatalf("content = %q", got)
	}
	if res, _ := svc.ReconcileStorage(ctx); res.Orphans != 0 || res.Requeued != 0 {
		t.Fatalf("after the rerun: %+v", res)
	}
}
