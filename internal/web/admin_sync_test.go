package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestSyncPage(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	e.seed()
	snap := scoresaber.LimiterSnapshot{Windows: []scoresaber.WindowSnapshot{
		{Name: "short", Limit: 20, Used: 3, ServerRemaining: -1},
		{Name: "medium", Limit: 60, Used: 9, ServerRemaining: -1},
		{Name: "long", Limit: 300, Used: 120, ServerRemaining: 200},
	}}
	e.status.st = archiver.Status{
		State: archiver.StateRunning, Task: "Downloading replay 42 · Alice · Song", Since: testutil.T0,
		Limiter: snap, Limiters: []archiver.LimiterStatus{{Platform: "ScoreSaber", Name: "scoresaber", Snapshot: snap}},
	}
	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, body, "Running", "Downloading replay 42", "Last hour", "120 / 300", "ScoreSaber reports 200 remaining",
		"ScoreSaber budget", "Alice", `hx-trigger="every 3s"`, `id="sync-events"`, `id="failed-replays"`)

	live := e.do(http.MethodGet, "/admin/sync/live", nil, withCookie(c), htmx("sync-live")).Body.String()
	if !strings.HasPrefix(strings.TrimSpace(live), `<div id="sync-live"`) || strings.Contains(live, "<html") {
		t.Fatalf("live partial wrong:\n%s", live)
	}
}

func TestSyncPauseResume(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ctx := context.Background()
	paused := e.do(http.MethodPost, "/admin/sync/pause", url.Values{}, withCookie(c), htmx("sync-live")).Body.String()
	contains(t, paused, "Resume", "Worker paused")
	if st, _ := e.svc.Settings(ctx); !st.WorkerPaused {
		t.Fatal("worker not paused")
	}
	resumed := e.do(http.MethodPost, "/admin/sync/resume", url.Values{}, withCookie(c), htmx("sync-live")).Body.String()
	contains(t, resumed, "Pause", "Worker resumed")
	if st, _ := e.svc.Settings(ctx); st.WorkerPaused {
		t.Fatal("worker still paused")
	}
}

func TestSyncEventsFilter(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ctx := context.Background()
	e.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindReplay, Message: "boom happened"})
	e.svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindScores, Message: "all fine"})
	body := e.do(http.MethodGet, "/admin/sync/events?level=error", nil, withCookie(c), htmx("sync-events")).Body.String()
	if !strings.Contains(body, "boom happened") || strings.Contains(body, "all fine") {
		t.Fatalf("level filter not applied:\n%s", body)
	}
}

func TestSyncFailedPager(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ctx := context.Background()
	p, err := e.svc.AddPlayer(ctx, "1001", "")
	if err != nil {
		t.Fatal(err)
	}
	const total = 52
	items := make([]scoresaber.ScoreItem, 0, total)
	for i := range total {
		items = append(items, testutil.Item("1001", int64(i+1), 900+int64(i), testutil.T0.Add(time.Duration(i)*time.Minute), true))
	}
	if _, err := e.svc.UpsertPlays(ctx, p.ID, model.PlatformScoreSaber, scoresaber.Plays(items)); err != nil {
		t.Fatal(err)
	}
	for range service.MaxReplayAttempts {
		for _, it := range items {
			if _, _, err := e.svc.MarkReplayAttemptFailed(ctx, it.Score.ID, errors.New("502 bad gateway")); err != nil {
				t.Fatal(err)
			}
		}
	}
	page1 := e.do(http.MethodGet, "/admin/sync/failed?page=1", nil, withCookie(c), htmx("failed-replays")).Body.String()
	contains(t, page1, "Page 1 of 2", `hx-get="/admin/sync/failed?page=2"`, "#52")
	page2 := e.do(http.MethodGet, "/admin/sync/failed?page=2", nil, withCookie(c), htmx("failed-replays")).Body.String()
	contains(t, page2, "Page 2 of 2", "#2", "#1")
	if strings.Contains(page2, "#52") {
		t.Fatalf("page 2 repeats page 1 rows:\n%s", page2)
	}
}

func TestSyncRetryFailed(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	e.seed()
	ctx := context.Background()
	for range service.MaxReplayAttempts {
		if _, _, err := e.svc.MarkReplayAttemptFailed(ctx, 2, errors.New("502 bad gateway")); err != nil {
			t.Fatal(err)
		}
	}
	page := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, page, "Retry all", "502 bad gateway")
	rec := e.do(http.MethodPost, "/admin/sync/retry", url.Values{"score_id": {"2"}}, withCookie(c), htmx("failed-replays"))
	contains(t, rec.Body.String(), `id="failed-replays"`, "Re-queued 1 replay", "No failed replays")
	if s, _ := e.svc.GetScore(ctx, 2); s.ReplayState != model.ReplayPending {
		t.Fatalf("state = %s", s.ReplayState)
	}
}

func TestSyncRequiresLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if rec := e.do(http.MethodGet, "/admin/sync", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestSyncQueuePerFeed(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	a := e.crossSeed()
	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, body, "Per-account progress", "ScoreSaber", "TestPlat")
	if n := strings.Count(body, `hx-post="/admin/players/`+a+`/poll"`); n != 2 {
		t.Fatalf("one queue row per feed: %d", n)
	}
}

// TestBudgetCardPerLimiter: a platform with several limiters (BeatLeader's API
// and CDN) gets one titled card per limiter.
func TestBudgetCardPerLimiter(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	win := func(limit, used int) platform.LimiterSnapshot {
		return platform.LimiterSnapshot{Windows: []platform.WindowSnapshot{{Name: "short", Limit: limit, Used: used, ServerRemaining: -1}}}
	}
	e.status.st = archiver.Status{State: archiver.StateIdle, Since: testutil.T0, Limiters: []archiver.LimiterStatus{
		{Platform: "TestPlat", Name: "testplat/api", Snapshot: win(40, 2)},
		{Platform: "TestPlat", Name: "testplat/cdn", Snapshot: win(20, 5)},
	}}
	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, body, "TestPlat API budget", "TestPlat CDN budget", "Last 10 seconds", "2 / 40", "5 / 20")
}

func TestSyncEventsFilterByPlatform(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	ctx := context.Background()
	e.svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Platform: new("testplat"), Feed: new(model.KindScore), Message: "tp polled"})
	e.svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Platform: new(model.PlatformScoreSaber), Feed: new(model.KindScore), Message: "ss polled"})
	tp := e.do(http.MethodGet, "/admin/sync/events?platform=testplat", nil, withCookie(c), htmx("sync-events")).Body.String()
	if !strings.Contains(tp, "tp polled") || strings.Contains(tp, "ss polled") {
		t.Fatalf("platform filter:\n%s", tp)
	}
	contains(t, tp, "TestPlat")
	if att := e.do(http.MethodGet, "/admin/sync/events?feed=attempt", nil, withCookie(c), htmx("sync-events")).Body.String(); strings.Contains(att, "polled") {
		t.Fatal("feed filter")
	}
	contains(t, e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String(), `name="platform"`, "All platforms", `name="feed"`, "All feeds")
}
