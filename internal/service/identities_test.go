package service_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestLinkIdentity(t *testing.T) {
	svc, _, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	<-svc.WakeC()
	clk.Advance(time.Hour)
	link, err := svc.LinkIdentity(ctx, alice, "https://tp.example/u/abc", "testplat")
	if err != nil {
		t.Fatal(err)
	}
	if link.ExternalID != "abc" || !link.Enabled || !link.LinkedAt.Equal(testutil.T0.Add(time.Hour)) {
		t.Fatalf("link = %+v", link)
	}
	sum, _ := svc.GetPlayerSummary(ctx, alice)
	if len(sum.Identities) != 2 || sum.Identities[0].Platform != model.PlatformScoreSaber || sum.Identities[1].Platform != "testplat" {
		t.Fatalf("identities = %+v", sum.Identities)
	}
	f := sum.Identities[1].Feed(model.KindScore)
	if !f.Enabled || f.BackfillState != model.BackfillPending || !f.StartedAt.Equal(testutil.T0.Add(time.Hour)) || f.Access != model.AccessNA {
		t.Fatalf("new account's feed = %+v", f)
	}
	if len(sum.Identities[1].Feeds) != 1 {
		t.Fatal("optional feeds are not created by linking")
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("linking must wake the worker")
	}
	if p, _ := svc.PlayerByIdentity(ctx, "testplat", "abc"); p == nil || p.ID != alice {
		t.Fatal("account lookup must find the player")
	}
}

func TestLinkIdentityRefusals(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	bob := mustAdd(t, svc, "1002")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LinkIdentity(ctx, alice, "def", "testplat"); !errors.Is(err, service.ErrPlatformAlreadyLinked) {
		t.Fatalf("second account on one platform: %v", err)
	}
	for _, ref := range []string{"abc", "https://tp.example/u/abc"} {
		_, err := svc.LinkIdentity(ctx, bob, ref, "testplat")
		var le *service.LinkedElsewhereError
		if !errors.As(err, &le) || le.PlayerID != alice || le.PlayerName != "Alice" || le.Platform != "TestPlat" ||
			!errors.Is(err, service.ErrIdentityLinkedElsewhere) {
			t.Fatalf("link %q to bob: %v", ref, err)
		}
	}
	if _, err := svc.AddPlayer(ctx, "https://tp.example/u/abc", ""); !errors.Is(err, service.ErrPlayerExists) || !errors.Is(err, service.ErrIdentityLinkedElsewhere) {
		t.Fatalf("adding an account linked as a secondary account: %v", err)
	}
	if _, err := svc.LinkIdentity(ctx, bob, "zzz", "testplat"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := svc.LinkIdentity(ctx, bob, "abc", ""); !errors.Is(err, service.ErrInvalidPlayerRef) {
		t.Fatalf("a link names its platform: %v", err)
	}
	if _, err := svc.LinkIdentity(ctx, "p99999999999", "def", "testplat"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown player: %v", err)
	}
	if sum, _ := svc.GetPlayerSummary(ctx, bob); len(sum.Identities) != 1 {
		t.Fatal("refusals must not create anything")
	}
}

func TestUnlinkIdentity(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true))
	testutil.UpsertFake(t, svc, alice,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0, false))
	t1 := testutil.Row(t, svc, alice, "t1")
	testutil.Archive(t, svc, t1, "tp bytes")
	tpPath, _ := svc.ReplayPath(t1)

	if err := svc.UnlinkIdentity(ctx, alice, "testplat", true); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.PlayerCounts(ctx, alice); c.Scores != 1 {
		t.Fatalf("only the ScoreSaber row must remain: %+v", c)
	}
	if feeds, _ := svc.Feeds(ctx, alice); len(feeds) != 1 || feeds[0].Platform != model.PlatformScoreSaber {
		t.Fatalf("feeds = %+v", feeds)
	}
	if _, err := os.Stat(tpPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("files of the unlinked platform must be deleted when asked")
	}
	if _, err := svc.PlayerByIdentity(ctx, "testplat", "abc"); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("the account must be free to link elsewhere")
	}
	if err := svc.UnlinkIdentity(ctx, alice, model.PlatformScoreSaber, false); !errors.Is(err, service.ErrLastIdentity) {
		t.Fatalf("last account: %v", err)
	}
	if err := svc.UnlinkIdentity(ctx, alice, "testplat", false); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestUnlinkLegacyKeepsFilesUnlessAsked(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true), testutil.Item("1001", 2, 502, testutil.T0, true))
	s1, _ := svc.GetScore(ctx, 1)
	s2, _ := svc.GetScore(ctx, 2)
	testutil.Archive(t, svc, s1, "one")
	testutil.Archive(t, svc, s2, "two")
	if err := svc.UnlinkIdentity(ctx, alice, model.PlatformScoreSaber, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().Open(storage.Loc{PlayerID: alice, RowID: 1, Ext: ".dat"}); err != nil {
		t.Fatal("files are kept unless asked")
	}
	if _, err := svc.GetScore(ctx, 1); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("rows of the unlinked account are removed")
	}
	// Relink and unlink again, deleting files: the legacy layout is removed file by file.
	if _, err := svc.LinkIdentity(ctx, alice, "1001", model.PlatformScoreSaber); err != nil {
		t.Fatal(err)
	}
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true))
	s1, _ = svc.GetScore(ctx, 1)
	testutil.Archive(t, svc, s1, "one again")
	if err := svc.UnlinkIdentity(ctx, alice, model.PlatformScoreSaber, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().Open(storage.Loc{PlayerID: alice, RowID: 1, Ext: ".dat"}); err == nil {
		t.Fatal("legacy file must be deleted when asked")
	}
}

func TestSetIdentityEnabled(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	_ = svc.MarkIdentityError(ctx, alice, "testplat", "player not found on TestPlat", true)
	if err := svc.SetIdentityEnabled(ctx, alice, "testplat", true); err != nil {
		t.Fatal(err)
	}
	sum, _ := svc.GetPlayerSummary(ctx, alice)
	if id := sum.Identities[1]; !id.Enabled || id.LastError != "" {
		t.Fatalf("resuming clears the error: %+v", id.PlayerPlatform)
	}
	if err := svc.SetIdentityEnabled(ctx, alice, "testplat", false); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.DueFeed(ctx, time.Minute, nil); f == nil || f.Platform != model.PlatformScoreSaber {
		t.Fatalf("a paused account is never due, the others are: %+v", f)
	}
	if err := svc.SetIdentityEnabled(ctx, alice, "nope", true); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestCountsPerAccountAndKind(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true), testutil.Item("1001", 2, 502, testutil.T0, false))
	testutil.UpsertFake(t, svc, alice,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0, true),
		testutil.FakePlay(model.KindAttempt, "a1", "lb-a", testutil.T0, true))
	testutil.Archive(t, svc, testutil.Row(t, svc, alice, "t1"), "x")
	sum, _ := svc.GetPlayerSummary(ctx, alice)
	if sum.Counts.Scores != 4 || sum.Counts.Archived != 1 || sum.Counts.Pending != 2 {
		t.Fatalf("headline counts are score rows of every platform: %+v", sum.Counts)
	}
	ss, tp := sum.Identities[0], sum.Identities[1]
	if ss.Scores().Scores != 2 || tp.Scores().Scores != 2 || tp.Scores().Archived != 1 || tp.Counts[model.KindAttempt].Scores != 1 {
		t.Fatalf("per-account counts: ss=%+v tp=%+v", ss.Counts, tp.Counts)
	}
	all, _ := svc.ListPlayers(ctx, true)
	if all[0].Identities[1].Scores().Scores != 2 {
		t.Fatal("ListPlayers fills per-account counts too")
	}
}
