package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

const hubTimeout = 2 * time.Second

func recv(t *testing.T, sub *service.Subscription) service.Update {
	t.Helper()
	select {
	case u := <-sub.C:
		return u
	case <-time.After(hubTimeout):
		t.Fatal("no update received")
		return service.Update{}
	}
}

func TestHubFanOut(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	a := svc.Subscribe(nil)
	defer a.Close()
	b := svc.Subscribe(nil)
	defer b.Close()

	svc.Publish(service.Update{PlayerID: "p1", Kind: model.KindReplay})

	for name, sub := range map[string]*service.Subscription{"a": a, "b": b} {
		if u := recv(t, sub); u.PlayerID != "p1" || u.Kind != model.KindReplay {
			t.Fatalf("%s got %+v", name, u)
		}
	}
}

func TestHubFilter(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	sub := svc.Subscribe(func(u service.Update) bool { return u.PlayerID == "p1" })
	defer sub.Close()

	svc.Publish(service.Update{PlayerID: "p2"})
	svc.Publish(service.Update{PlayerID: "p1", Kind: model.KindScores})

	if u := recv(t, sub); u.PlayerID != "p1" {
		t.Fatalf("filtered subscriber got %+v", u)
	}
}

func TestHubNilFilterMatchesAll(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	sub := svc.Subscribe(nil)
	defer sub.Close()

	svc.Publish(service.Update{}) // global update

	if u := recv(t, sub); u.PlayerID != "" {
		t.Fatalf("global update = %+v", u)
	}
}

// A subscriber that stops reading must never block the publisher; updates
// beyond the buffer are dropped.
func TestHubSlowSubscriberDrops(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	sub := svc.Subscribe(nil)

	for range 100 {
		svc.Publish(service.Update{PlayerID: "p1"})
	}

	var got int
	for {
		select {
		case <-sub.C:
			got++
			continue
		default:
		}
		break
	}
	sub.Close()
	if got == 0 || got > service.UpdateBuffer {
		t.Fatalf("got %d updates, want 1..%d", got, service.UpdateBuffer)
	}
}

func TestHubClose(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	sub := svc.Subscribe(nil)
	sub.Close()
	sub.Close() // idempotent
	svc.Publish(service.Update{PlayerID: "p1"})
	if _, ok := <-sub.C; ok {
		t.Fatal("closed subscription channel must be closed")
	}
}

func TestLogPublishesUpdate(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	sub := svc.Subscribe(nil)
	defer sub.Close()

	svc.Log(context.Background(), model.SyncEvent{Kind: model.KindReplay, PlayerID: new("p1"), Message: "archived replay"})

	if u := recv(t, sub); u.PlayerID != "p1" || u.Kind != model.KindReplay {
		t.Fatalf("Log must publish the update, got %+v", u)
	}
}

func TestRetryFailedPublishesUpdate(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	p := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	testutil.UpsertFake(t, svc, p, testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true))
	row := testutil.Row(t, svc, p, "t1")
	for range service.MaxReplayAttempts {
		if _, _, err := svc.MarkReplayAttemptFailed(ctx, row.ID, context.DeadlineExceeded); err != nil {
			t.Fatal(err)
		}
	}
	sub := svc.Subscribe(nil)
	defer sub.Close()

	if n, err := svc.RetryFailed(ctx, p, 0); err != nil || n != 1 {
		t.Fatalf("RetryFailed = %d %v", n, err)
	}

	if u := recv(t, sub); u.PlayerID != p {
		t.Fatalf("RetryFailed must publish for the player, got %+v", u)
	}
}

func TestSetWorkerPausedPublishesUpdate(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	sub := svc.Subscribe(nil)
	defer sub.Close()

	if err := svc.SetWorkerPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if u := recv(t, sub); u.PlayerID != "" {
		t.Fatalf("pause must publish a global update, got %+v", u)
	}
}

func TestSetPlayerEnabledPublishesUpdate(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	p := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	sub := svc.Subscribe(nil)
	defer sub.Close()

	if err := svc.SetPlayerEnabled(ctx, p, false); err != nil {
		t.Fatal(err)
	}

	if u := recv(t, sub); u.PlayerID != p {
		t.Fatalf("SetPlayerEnabled must publish for the player, got %+v", u)
	}
}
