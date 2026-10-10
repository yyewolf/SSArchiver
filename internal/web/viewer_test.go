package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func ctx() context.Context { return context.Background() }

// The BeatLeader viewer only loads replay links ending in .bsor, so every
// open-replay platform also serves its replays under that extension.

func TestReplayBSORAlias(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	e.seed()
	e.seedTP()
	if rec := e.do(http.MethodGet, "/r/1.bsor", nil); rec.Code != 200 || rec.Body.String() != replayBody {
		t.Fatalf("legacy .bsor alias = %d %q", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/r/tp/t1.bsor", nil); rec.Code != 200 || rec.Body.String() != "TP replay bytes" {
		t.Fatalf("platform .bsor alias = %d %q", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/r/tp/attempt/a1.bsor", nil); rec.Code != 200 || rec.Body.String() != "TP attempt bytes" {
		t.Fatalf("attempt .bsor alias = %d %q", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/r/tp/t2.bsor", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("pending replay alias = %d", rec.Code)
	}
}

func TestReplayBSORAliasNeedsBSORPlatform(t *testing.T) {
	fp := testutil.NewFakePlatformAs("nonbsor", "nb", "NoBSOR")
	fp.BSOR = false
	e := newEnvWith(t, fp)
	e.setup()
	p := testutil.AddPlayer(e.t, e.svc, "https://nb.example/u/abc")
	if _, err := e.svc.UpsertPlays(ctx(), p, "nonbsor", []platform.Play{
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
	}); err != nil {
		e.t.Fatal(err)
	}
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, p, "t1"), "NB replay bytes")
	if rec := e.do(http.MethodGet, "/r/nb/t1.bsor", nil); rec.Code != http.StatusNotFound {
		t.Fatalf(".bsor on a non-BSOR platform = %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/r/nb/t1.tpr", nil); rec.Code != 200 {
		t.Fatalf("native extension = %d", rec.Code)
	}
}

// The embed page defaults to the instance's viewer (BeatLeader unless the
// admin changes it); ?viewer= overrides it.

func TestEmbedViewerChoice(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	ctx := context.Background()

	rec := e.do(http.MethodGet, "/embed/1", nil)
	contains(t, rec.Body.String(),
		`src="https://replay.beatleader.com/?link=https%3A%2F%2Freplays.example.com%2Fr%2F1.bsor"`)
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-src 'self' https://replay.beatleader.com") {
		t.Fatalf("embed CSP must allow the BeatLeader viewer: %q", rec.Header().Get("Content-Security-Policy"))
	}

	opts := e.do(http.MethodGet, "/embed/1?viewer=beatleader&autoplay=1&loop=1", nil)
	contains(t, opts.Body.String(),
		"autoplay=true", "loop=true", "link=https%3A%2F%2Freplays.example.com%2Fr%2F1.bsor")

	arc := e.do(http.MethodGet, "/embed/1?viewer=arcviewer&autoplay=1&loop=1&ui=0", nil)
	contains(t, arc.Body.String(), "/viewer/?autoPlay=true", "loop=true", "uiOff=true",
		"replayURL=https%3A%2F%2Freplays.example.com%2Fr%2F1.dat")

	if bad := e.do(http.MethodGet, "/embed/1?viewer=arc", nil); !strings.Contains(bad.Body.String(), "replay.beatleader.com") {
		t.Fatal("an unknown ?viewer= must fall back to the default viewer")
	}

	st, _ := e.svc.Settings(ctx)
	st.ReplayViewer = service.ViewerArcViewer
	if err := e.svc.UpdateViewerSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	contains(t, e.do(http.MethodGet, "/embed/1", nil).Body.String(),
		"/viewer/?noProxy=true", "replayURL=https%3A%2F%2Freplays.example.com%2Fr%2F1.dat")
	contains(t, e.do(http.MethodGet, "/embed/1?viewer=beatleader", nil).Body.String(),
		"replay.beatleader.com/?link=")
}

func TestEmbedViewerWithoutArcBuild(t *testing.T) {
	// A build without the bundled ArcViewer still embeds the hosted viewer;
	// an explicit arc choice falls back to it.
	e := newEnvNoViewer(t)
	e.setup()
	e.seed()
	contains(t, e.do(http.MethodGet, "/embed/1", nil).Body.String(), "replay.beatleader.com/?link=")
	contains(t, e.do(http.MethodGet, "/embed/1?viewer=arcviewer", nil).Body.String(), "replay.beatleader.com/?link=")
}

// Score pages resolve the viewer from ?viewer= (which remembers it in a
// cookie), then the cookie, then the instance default.

func TestScorePageViewerChoice(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()

	page := e.do(http.MethodGet, "/s/1", nil)
	contains(t, page.Body.String(),
		`src="/embed/1?viewer=beatleader"`, `href="/embed/1?viewer=beatleader"`,
		`href="?viewer=arcviewer"`,
		"https://replays.example.com/embed/1?viewer=beatleader")

	switched := e.do(http.MethodGet, "/s/1?viewer=arcviewer", nil)
	contains(t, switched.Body.String(), `src="/embed/1?viewer=arcviewer"`, `href="?viewer=beatleader"`)
	var cookie *http.Cookie
	for _, c := range switched.Result().Cookies() {
		if c.Name == "ssa_viewer" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != "arcviewer" || cookie.MaxAge < 24*3600 {
		t.Fatalf("viewer cookie = %v", cookie)
	}

	recalled := e.do(http.MethodGet, "/s/1", nil, withCookie(&http.Cookie{Name: "ssa_viewer", Value: "arcviewer"}))
	contains(t, recalled.Body.String(), `src="/embed/1?viewer=arcviewer"`)

	back := e.do(http.MethodGet, "/s/1?viewer=beatleader", nil, withCookie(&http.Cookie{Name: "ssa_viewer", Value: "arcviewer"}))
	contains(t, back.Body.String(), `src="/embed/1?viewer=beatleader"`)

	ignored := e.do(http.MethodGet, "/s/1?viewer=nonsense", nil, withCookie(&http.Cookie{Name: "ssa_viewer", Value: "arcviewer"}))
	contains(t, ignored.Body.String(), `src="/embed/1?viewer=arcviewer"`)
}

// A row the BeatLeader viewer cannot load (non open-replay platform) falls
// back to ArcViewer and hides the BeatLeader option.

func TestScorePageViewerFallback(t *testing.T) {
	fp := testutil.NewFakePlatformAs("nonbsor", "nb", "NoBSOR")
	fp.BSOR = false
	e := newEnvWith(t, fp)
	e.setup()
	p := testutil.AddPlayer(e.t, e.svc, "https://nb.example/u/abc")
	if _, err := e.svc.UpsertPlays(ctx(), p, "nonbsor", []platform.Play{
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
	}); err != nil {
		e.t.Fatal(err)
	}
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, p, "t1"), "NB replay bytes")

	page := e.do(http.MethodGet, "/s/nb/t1", nil)
	contains(t, page.Body.String(), `src="/embed/nb/t1?viewer=arcviewer"`)
	if strings.Contains(page.Body.String(), "?viewer=beatleader") {
		t.Fatal("a non-BSOR row must not offer the BeatLeader viewer")
	}
}
