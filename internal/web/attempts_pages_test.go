package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

// seedAttempts is seedTP plus a failed attempt with a replay (a2) and a quit
// without one (a3), on t1's map.
func (e *testEnv) seedAttempts() string {
	e.t.Helper()
	tess := e.seedTP()
	fail := testutil.FakePlay(model.KindAttempt, "a2", "lb-a", testutil.T0.Add(-time.Minute), true)
	fail.EndType, fail.EndTime = model.EndFail, new(59.74)
	quit := testutil.FakePlay(model.KindAttempt, "a3", "lb-a", testutil.T0.Add(-2*time.Minute), false)
	quit.EndType, quit.EndTime = model.EndQuit, new(12.0)
	testutil.UpsertFake(e.t, e.svc, tess, fail, quit)
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, tess, "a2"), "fail bytes")
	return tess
}

func TestAttemptPage(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	e.seedAttempts()

	page := e.do(http.MethodGet, "/s/tp/attempt/a2", nil)
	body := page.Body.String()
	if page.Code != 200 {
		t.Fatalf("attempt page = %d", page.Code)
	}
	contains(t, body, "Failed", "Ended at", "0:59", "Download .tpr", "Failed attempt by Tess")
	if hasViewer := strings.Contains(body, `src="/embed/tp/attempt/a2"`); hasViewer != views.AttemptViewer {
		t.Fatalf("viewer shown = %v, AttemptViewer = %v", hasViewer, views.AttemptViewer)
	}
	embed := e.do(http.MethodGet, "/embed/tp/attempt/a2", nil).Code
	if (embed == 200) != views.AttemptViewer {
		t.Fatalf("embed = %d with AttemptViewer = %v", embed, views.AttemptViewer)
	}
	contains(t, e.do(http.MethodGet, "/s/tp/attempt/a3", nil).Body.String(), "Quit", "TestPlat kept no replay for this attempt.")
	if got := e.do(http.MethodGet, "/r/tp/attempt/a2.tpr", nil).Body.String(); got != "fail bytes" {
		t.Fatalf("download = %q", got)
	}
}

func TestAttemptChipsAndCounts(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	tess := e.seedAttempts()
	ctx := context.Background()
	if _, err := e.svc.SetFeedEnabled(ctx, service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindAttempt}, true); err != nil {
		t.Fatal(err)
	}
	page := e.do(http.MethodGet, "/p/"+tess, nil).Body.String()
	contains(t, page, "2 attempts archived") // a1 (seedTP) and a2
	frag := e.do(http.MethodGet, "/p/"+tess+"/map?key=hash-lb-a%2FStandard%2F7&type=all", nil, htmx("plays-0")).Body.String()
	contains(t, frag, `href="/s/tp/attempt/a2"`, "Failed", `href="/s/tp/attempt/a3"`, "Quit")
}
