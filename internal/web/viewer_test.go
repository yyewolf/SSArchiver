package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
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

	rec := e.do(http.MethodGet, "/embed/1", nil)
	if rec.Code != 200 {
		t.Fatalf("embed = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-src 'self' https://replay.beatleader.com") {
		t.Fatalf("embed CSP must allow the BeatLeader viewer: %q", rec.Header().Get("Content-Security-Policy"))
	}
	contains(t, rec.Body.String(), "/viewer/?noProxy=true", "replayURL=https%3A%2F%2Freplays.example.com%2Fr%2F1.dat")

	opts := e.do(http.MethodGet, "/embed/1?viewer=beatleader&autoplay=1&loop=1", nil)
	contains(t, opts.Body.String(),
		"autoplay=true", "loop=true", "link=https%3A%2F%2Freplays.example.com%2Fr%2F1.bsor")

	arc := e.do(http.MethodGet, "/embed/1?viewer=arcviewer&autoplay=1&loop=1&ui=0", nil)
	contains(t, arc.Body.String(), "/viewer/?autoPlay=true", "loop=true", "uiOff=true",
		"replayURL=https%3A%2F%2Freplays.example.com%2Fr%2F1.dat")

	if bad := e.do(http.MethodGet, "/embed/1?viewer=arc", nil); strings.Contains(bad.Body.String(), "replay.beatleader.com") {
		t.Fatal("an unknown ?viewer= must fall back to the platform default viewer")
	}

	// BeatLeader rows default to their own viewer.
	bl := newEnvWith(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"))
	bl.setup()
	p := testutil.AddPlayer(bl.t, bl.svc, "https://bl.example/u/abc")
	if _, err := bl.svc.UpsertPlays(ctx(), p, "beatleader", []platform.Play{
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
	}); err != nil {
		bl.t.Fatal(err)
	}
	testutil.Archive(bl.t, bl.svc, testutil.Row(bl.t, bl.svc, p, "t1"), "BL replay bytes")
	contains(t, bl.do(http.MethodGet, "/embed/bl/t1", nil).Body.String(), "replay.beatleader.com/?link=")
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

// The no-choice default viewer follows the row's platform: BeatLeader's
// hosted viewer for BeatLeader rows, the bundled ArcViewer for the rest.
// ?viewer= (remembered in a cookie) overrides it.

func TestScorePageViewerChoice(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()

	page := e.do(http.MethodGet, "/s/1", nil)
	contains(t, page.Body.String(),
		`src="/embed/1?viewer=arcviewer"`, `href="/embed/1?viewer=arcviewer"`,
		`href="?viewer=beatleader"`,
		"https://replays.example.com/embed/1?viewer=arcviewer")

	switched := e.do(http.MethodGet, "/s/1?viewer=beatleader", nil)
	contains(t, switched.Body.String(), `src="/embed/1?viewer=beatleader"`, `href="?viewer=arcviewer"`)
	var cookie *http.Cookie
	for _, c := range switched.Result().Cookies() {
		if c.Name == "ssa_viewer" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != "beatleader" || cookie.MaxAge < 24*3600 {
		t.Fatalf("viewer cookie = %v", cookie)
	}

	recalled := e.do(http.MethodGet, "/s/1", nil, withCookie(&http.Cookie{Name: "ssa_viewer", Value: "beatleader"}))
	contains(t, recalled.Body.String(), `src="/embed/1?viewer=beatleader"`)

	back := e.do(http.MethodGet, "/s/1?viewer=arcviewer", nil, withCookie(&http.Cookie{Name: "ssa_viewer", Value: "beatleader"}))
	contains(t, back.Body.String(), `src="/embed/1?viewer=arcviewer"`)

	ignored := e.do(http.MethodGet, "/s/1?viewer=nonsense", nil, withCookie(&http.Cookie{Name: "ssa_viewer", Value: "beatleader"}))
	contains(t, ignored.Body.String(), `src="/embed/1?viewer=beatleader"`)
}

func TestScorePageViewerPlatformDefault(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"))
	e.setup()
	p := testutil.AddPlayer(e.t, e.svc, "https://bl.example/u/abc")
	if _, err := e.svc.UpsertPlays(ctx(), p, "beatleader", []platform.Play{
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
	}); err != nil {
		e.t.Fatal(err)
	}
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, p, "t1"), "BL replay bytes")
	contains(t, e.do(http.MethodGet, "/s/bl/t1", nil).Body.String(), `src="/embed/bl/t1?viewer=beatleader"`)
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
