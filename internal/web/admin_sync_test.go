package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestSyncPage(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	e.seed()
	e.status.st = archiver.Status{
		State: archiver.StateRunning, Task: "Downloading replay 42 · Alice · Song", Since: testutil.T0,
		Limiter: scoresaber.LimiterSnapshot{Windows: []scoresaber.WindowSnapshot{
			{Name: "short", Limit: 20, Used: 3, ServerRemaining: -1},
			{Name: "medium", Limit: 60, Used: 9, ServerRemaining: -1},
			{Name: "long", Limit: 300, Used: 120, ServerRemaining: 200},
		}},
	}
	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, body, "Running", "Downloading replay 42", "Last hour", "120 / 300", "ScoreSaber reports 200 remaining",
		"Alice", `hx-trigger="every 3s"`, `id="sync-events"`, `id="failed-replays"`)

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
