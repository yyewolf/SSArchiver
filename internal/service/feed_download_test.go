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

// newScoreFeed tracks a player and stores one pending replay on its score feed.
func newScoreFeed(t *testing.T) (*service.Service, string) {
	t.Helper()
	svc, _, _ := testutil.NewMultiService(t)
	p := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	testutil.UpsertFake(t, svc, p, testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true))
	return svc, p
}

// testplatScoreFeed is the score feed of a player linked on the fake platform.
func testplatScoreFeed(playerID string) service.FeedKey {
	return service.FeedKey{PlayerID: playerID, Platform: "testplat", Kind: model.KindScore}
}

func feedOf(t *testing.T, svc *service.Service, playerID string) model.SyncFeed {
	t.Helper()
	return testutil.ScoreFeed(t, svc, playerID)
}

func TestSetFeedDownload(t *testing.T) {
	svc, p := newScoreFeed(t)
	ctx := context.Background()
	k := testplatScoreFeed(p)

	if feedOf(t, svc, p).DownloadEnabled != true {
		t.Fatal("a fresh feed must download replays")
	}

	sub := svc.Subscribe(nil)
	defer sub.Close()

	f, err := svc.SetFeedDownload(ctx, k, false)
	if err != nil || f.DownloadEnabled {
		t.Fatalf("SetFeedDownload(false) = %+v %v", f, err)
	}
	if feedOf(t, svc, p).DownloadEnabled {
		t.Fatal("the pause must persist")
	}
	select {
	case u := <-sub.C:
		if u.PlayerID != p {
			t.Fatalf("update = %+v", u)
		}
	case <-time.After(hubTimeout):
		t.Fatal("SetFeedDownload must publish an update")
	}

	if f, err := svc.SetFeedDownload(ctx, k, true); err != nil || !f.DownloadEnabled {
		t.Fatalf("SetFeedDownload(true) = %+v %v", f, err)
	}
}

func TestSetFeedDownloadUnknownFeed(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	if _, err := svc.SetFeedDownload(context.Background(), service.FeedKey{PlayerID: "p00000000001", Platform: model.PlatformScoreSaber, Kind: model.KindScore}, false); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestNextReplaySkipsPausedFeed(t *testing.T) {
	svc, p := newScoreFeed(t)
	ctx := context.Background()
	k := testplatScoreFeed(p)

	if sc, err := svc.NextReplay(ctx, service.TierNew, "", nil); err != nil || sc == nil {
		t.Fatalf("before pause: %v %v", sc, err)
	}

	if _, err := svc.SetFeedDownload(ctx, k, false); err != nil {
		t.Fatal(err)
	}
	if sc, err := svc.NextReplay(ctx, service.TierNew, "", nil); err != nil || sc != nil {
		t.Fatalf("a paused feed must not offer replays, got %v %v", sc, err)
	}
	if sc, err := svc.NextReplay(ctx, service.TierBackfill, "", nil); err != nil || sc != nil {
		t.Fatalf("paused feed offered a backfill replay: %v %v", sc, err)
	}

	if _, err := svc.SetFeedDownload(ctx, k, true); err != nil {
		t.Fatal(err)
	}
	if sc, err := svc.NextReplay(ctx, service.TierNew, "", nil); err != nil || sc == nil {
		t.Fatalf("after resume: %v %v", sc, err)
	}
}
