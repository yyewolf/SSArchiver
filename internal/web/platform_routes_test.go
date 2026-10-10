package web_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestPlatformRoutes(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	e.seedTP()

	page := e.do(http.MethodGet, "/s/tp/t1", nil)
	if page.Code != 200 {
		t.Fatalf("score page = %d", page.Code)
	}
	contains(t, page.Body.String(), "Song lb-a", `src="/embed/tp/t1?viewer=arcviewer"`, `href="/embed/tp/t1?viewer=arcviewer"`,
		`href="/r/tp/t1.tpr"`, "Download .tpr",
		"replays.example.com/embed/tp/t1?viewer=arcviewer", `property="og:url" content="https://replays.example.com/s/tp/t1"`)
	contains(t, e.do(http.MethodGet, "/s/tp/t2", nil).Body.String(), "queued for archiving")
	attempt := e.do(http.MethodGet, "/s/tp/attempt/a1", nil)
	if attempt.Code != 200 {
		t.Fatalf("attempt page = %d", attempt.Code)
	}
	contains(t, attempt.Body.String(), `src="/embed/tp/attempt/a1?viewer=arcviewer"`, `href="/r/tp/attempt/a1.tpr"`)

	raw := e.do(http.MethodGet, "/r/tp/t1.tpr", nil)
	h := raw.Header()
	if raw.Code != 200 || raw.Body.String() != "TP replay bytes" || h.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(h.Get("Cache-Control"), "immutable") || len(h.Get("ETag")) != 66 ||
		h.Get("Content-Disposition") != `attachment; filename="t1.tpr"` {
		t.Fatalf("raw = %d %q %v", raw.Code, raw.Body.String(), h)
	}
	if part := e.do(http.MethodGet, "/r/tp/t1.tpr", nil, withHeader("Range", "bytes=0-1")); part.Code != http.StatusPartialContent || part.Body.String() != "TP" {
		t.Fatalf("range = %d %q", part.Code, part.Body.String())
	}
	if got := e.do(http.MethodGet, "/r/tp/attempt/a1.tpr", nil).Body.String(); got != "TP attempt bytes" {
		t.Fatalf("attempt replay = %q", got)
	}
	if pre := e.do(http.MethodOptions, "/r/tp/attempt/a1.tpr", nil); pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight = %d", pre.Code)
	}

	embed := e.do(http.MethodGet, "/embed/tp/t1", nil)
	if embed.Code != 200 || embed.Header().Get("Content-Security-Policy") != httpx.EmbedCSP {
		t.Fatalf("embed = %d %q", embed.Code, embed.Header().Get("Content-Security-Policy"))
	}
	contains(t, embed.Body.String(), "replayURL=https%3A%2F%2Freplays.example.com%2Fr%2Ftp%2Ft1.tpr")
	contains(t, e.do(http.MethodGet, "/embed/tp/t1?viewer=beatleader", nil).Body.String(),
		"link=https%3A%2F%2Freplays.example.com%2Fr%2Ftp%2Ft1.bsor")

	for _, p := range []string{
		"/s/zz/t1", "/s/tp/nope", "/s/tp/attempt/t1", "/r/zz/t1.tpr", "/r/tp/t1.dat", "/r/tp/t2.tpr", "/r/tp/.tpr",
		"/embed/tp/t2", "/embed/zz/t1",
	} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
}

// TestLegacyRoutesResolveScoreSaberOnly: the bare routes of the original
// version must keep resolving ScoreSaber scores, and only them (spec §6.1).
func TestLegacyRoutesResolveScoreSaberOnly(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	e.seed()
	tess := e.seedTP()
	internal := strconv.FormatInt(testutil.Row(t, e.svc, tess, "t1").ID, 10)
	for _, p := range []string{"/s/" + internal, "/r/" + internal + ".dat", "/embed/" + internal} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d: a non-legacy row must not resolve through its internal ID", p, rec.Code)
		}
	}
	for _, p := range []string{"/s/1", "/embed/1"} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != 200 {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
	if got := e.do(http.MethodGet, "/r/1.dat", nil).Body.String(); got != replayBody {
		t.Errorf("/r/1.dat = %q", got)
	}
}

func TestCSPIncludesPlatformImageHosts(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	csp := e.do(http.MethodGet, "/", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: https://cdn.scoresaber.com https://img.tp.example;") {
		t.Fatalf("csp = %q", csp)
	}
}
