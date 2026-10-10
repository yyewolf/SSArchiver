package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestPlayerPage(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.seed()
	rec := e.do(http.MethodGet, "/p/"+a, nil)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	contains(t, rec.Body.String(), "Alice", "Hell of a time", "Song 502", "Archived", "Pending",
		`property="og:title"`, `href="/s/1"`, `id="score-filters"`, "Archiving replays")
}

func TestPlayerPageProfileLinkUsesAccountID(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.seed()
	rec := e.do(http.MethodGet, "/p/"+a, nil)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	contains(t, body, `href="https://scoresaber.com/u/1001"`, "ScoreSaber profile")
	if strings.Contains(body, "/u/"+a) {
		t.Fatalf("profile link leaks the opaque player ID %q:\n%s", a, body)
	}
}

func TestPlayerPageHTMXPartialAndFilters(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.seed()
	rec := e.do(http.MethodGet, "/p/"+a+"?state=archived", nil, htmx("scores"))
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), `<div id="scores"`) || strings.Contains(body, "<html") {
		t.Fatalf("expected a bare #scores partial, got:\n%s", body)
	}
	if !strings.Contains(body, "Hell of a time") || strings.Contains(body, "Song 502") {
		t.Fatalf("state filter not applied:\n%s", body)
	}
	search := e.do(http.MethodGet, "/p/"+a+"?q=hell", nil, htmx("scores")).Body.String()
	if !strings.Contains(search, "Hell of a time") || strings.Contains(search, "Song 503") {
		t.Fatal("search filter not applied")
	}
	ranked := e.do(http.MethodGet, "/p/"+a+"?ranked=1", nil, htmx("scores")).Body.String()
	if !strings.Contains(ranked, "Hell of a time") || strings.Contains(ranked, "Song 502") {
		t.Fatal("ranked filter not applied")
	}
	none := e.do(http.MethodGet, "/p/"+a+"?q=zzzz", nil, htmx("scores")).Body.String()
	contains(t, none, "No scores match")
}

// crossSeed is seed() plus Alice's testplat account: t1 on the same map as
// ScoreSaber score 1 (map 501), and t0, an older non-PB play of that map.
func (e *testEnv) crossSeed() string {
	e.t.Helper()
	a := e.seed()
	if _, err := e.svc.LinkIdentity(context.Background(), a, "abc", "testplat"); err != nil {
		e.t.Fatal(err)
	}
	t1 := testutil.FakePlay(model.KindScore, "t1", "lb-x501", testutil.T0.Add(4*time.Minute), true)
	t1.Leaderboard.SongHash, t1.Leaderboard.GameMode, t1.Leaderboard.Difficulty = "hash501", "Standard", 9
	t1.ModifiedScore = 950_000
	t0 := testutil.FakePlay(model.KindScore, "t0", "lb-x501", testutil.T0.Add(-time.Hour), false)
	t0.Leaderboard, t0.PersonalBest = t1.Leaderboard, false
	testutil.UpsertFake(e.t, e.svc, a, t1, t0)
	return a
}

func TestMergedPlayerPage(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.crossSeed()
	body := e.do(http.MethodGet, "/p/"+a, nil).Body.String()
	contains(t, body, `href="/s/1"`, `href="/s/tp/t1"`, "TestPlat", "ScoreSaber", "950,000",
		`href="/p/ss/1001"`, `href="/p/tp/abc"`, `href="https://tp.example/u/abc"`, "TestPlat profile",
		"1 more play", `hx-get="/p/`+a+`/map?key=hash501%2FStandard%2F9"`, `hx-target="#plays-0"`,
		"ScoreSaber &amp; TestPlat replays", `name="platform"`, "All platforms", `name="min_score"`, `name="max_score"`)
	if strings.Count(body, `href="/s/tp/t0"`) != 0 {
		t.Fatal("non-PB plays are behind 'more plays', not chips")
	}
}

func TestPlayerPageMapFragment(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.crossSeed()
	frag := e.do(http.MethodGet, "/p/"+a+"/map?key=hash501%2FStandard%2F9", nil, htmx("plays-0"))
	body := frag.Body.String()
	if frag.Code != 200 || strings.Contains(body, "<html") {
		t.Fatalf("fragment = %d\n%s", frag.Code, body)
	}
	contains(t, body, `href="/s/1"`, `href="/s/tp/t1"`, `href="/s/tp/t0"`, "superseded")
	tpOnly := e.do(http.MethodGet, "/p/"+a+"/map?key=hash501%2FStandard%2F9&platform=testplat", nil, htmx("plays-0")).Body.String()
	if strings.Contains(tpOnly, `href="/s/1"`) {
		t.Fatal("the fragment applies the page's filters")
	}
	for _, p := range []string{"/p/" + a + "/map", "/p/nobody/map?key=x"} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
}

// /p/{mergedAway}/map follows the merge alias and serves the survivor's plays.
func TestPlayerPageMapFragmentFollowsMerge(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.seed()
	tess := testutil.AddPlayer(e.t, e.svc, "https://tp.example/u/def")
	moved := testutil.FakePlay(model.KindScore, "tm1", "lb-m", testutil.T0.Add(5*time.Minute), true)
	moved.Leaderboard.SongHash, moved.Leaderboard.GameMode, moved.Leaderboard.Difficulty = "hash501", "Standard", 9
	testutil.UpsertFake(e.t, e.svc, tess, moved)
	if err := e.svc.MergePlayers(context.Background(), tess, a); err != nil {
		t.Fatal(err)
	}
	alias := e.do(http.MethodGet, "/p/"+tess+"/map?key=hash501%2FStandard%2F9", nil, htmx("plays-0"))
	if alias.Code != 200 || strings.Contains(alias.Body.String(), "<html") {
		t.Fatalf("alias fragment = %d\n%s", alias.Code, alias.Body.String())
	}
	contains(t, alias.Body.String(), `href="/s/1"`, `href="/s/tp/tm1"`)
}

func TestPlayerPagePlatformAndScoreFilters(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.crossSeed()
	get := func(q string) string {
		return e.do(http.MethodGet, "/p/"+a+"?"+q, nil, htmx("scores")).Body.String()
	}
	tp := get("platform=testplat")
	if !strings.Contains(tp, `href="/s/tp/t1"`) || strings.Contains(tp, `href="/s/1"`) || strings.Contains(tp, "Song 502") {
		t.Fatalf("platform filter:\n%s", tp)
	}
	if high := get("min_score=960000"); !strings.Contains(high, "Song 502") {
		t.Fatal("min_score keeps the ScoreSaber maps")
	}
	low := get("max_score=950000")
	if strings.Contains(low, "Song 502") || !strings.Contains(low, `href="/s/tp/t1"`) {
		t.Fatalf("max_score:\n%s", low)
	}
	if bad := get("platform=nope&min_score=abc"); !strings.Contains(bad, "Song 502") {
		t.Fatal("unknown values are ignored, not errors")
	}
}

func TestSinglePlatformPageHasNoPlatformFilter(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.seed()
	if body := e.do(http.MethodGet, "/p/"+a, nil).Body.String(); strings.Contains(body, `name="platform"`) {
		t.Fatal("the platform select only appears for players with several accounts")
	}
}

func TestMergedAwayPlayerRedirects(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.seed()
	tess := e.seedTP()
	if err := e.svc.MergePlayers(context.Background(), tess, a); err != nil {
		t.Fatal(err)
	}
	res := e.do(http.MethodGet, "/p/"+tess+"?page=2&q=x", nil)
	if res.Code != http.StatusMovedPermanently || res.Header().Get("Location") != "/p/"+a+"?page=2&q=x" {
		t.Fatalf("alias = %d %q", res.Code, res.Header().Get("Location"))
	}
	if loc := e.do(http.MethodGet, "/p/tp/abc", nil).Header().Get("Location"); loc != "/p/"+a {
		t.Fatalf("account links follow the merge: %q", loc)
	}
}

func TestPlayerNotFound(t *testing.T) {
	e := newEnv(t)
	e.setup()
	rec := e.do(http.MethodGet, "/p/424242", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
	contains(t, rec.Body.String(), "not archived here")
}

func TestAccountLinkRedirects(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	res := e.do(http.MethodGet, "/p/ss/1001", nil)
	if res.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d", res.Code)
	}
	id := e.playerID("1001")
	if loc := res.Header().Get("Location"); loc != "/p/"+id {
		t.Fatalf("Location = %q, want /p/%s", loc, id)
	}
	for _, path := range []string{"/p/ss/9999", "/p/zz/1001"} {
		if got := e.do(http.MethodGet, path, nil).Code; got != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, got)
		}
	}
}

func TestScorePageArchived(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	rec := e.do(http.MethodGet, "/s/1", nil)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	contains(t, rec.Body.String(),
		"Hell of a time", `src="/embed/1"`, `href="/r/1.dat"`, "Download .dat",
		"replays.example.com/embed/1", `data-copy="#embed-code"`, "sha256",
		`property="og:image" content="https://cdn.scoresaber.com/covers/x.png"`, "Ranked")
}

func TestPlayerPageTypeFilter(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	tess := e.seedTP() // t1 and t2 scores, attempt a1 (a clear) on t1's map
	fail := testutil.FakePlay(model.KindAttempt, "a2", "lb-c", testutil.T0.Add(-time.Hour), true)
	fail.EndType = model.EndFail
	testutil.UpsertFake(t, e.svc, tess, fail)

	page := e.do(http.MethodGet, "/p/"+tess, nil).Body.String()
	contains(t, page, `name="type"`, "Completed", "Fails", "Everything")
	if strings.Contains(page, "Song lb-c") {
		t.Fatal("a map with only a failed attempt is hidden by default")
	}
	fails := e.do(http.MethodGet, "/p/"+tess+"?type=fail", nil, htmx("scores")).Body.String()
	if !strings.Contains(fails, "Song lb-c") || strings.Contains(fails, "Song lb-b") {
		t.Fatalf("type=fail:\n%s", fails)
	}
	if bogus := e.do(http.MethodGet, "/p/"+tess+"?type=bogus", nil, htmx("scores")).Body.String(); strings.Contains(bogus, "Song lb-c") {
		t.Fatal("an invalid type falls back to the default")
	}
	alice := e.seed()
	if strings.Contains(e.do(http.MethodGet, "/p/"+alice, nil).Body.String(), `name="type"`) {
		t.Fatal("players without attempts get no type select")
	}
}

func TestScorePageStates(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	pending := e.do(http.MethodGet, "/s/2", nil).Body.String()
	contains(t, pending, "queued for archiving")
	if strings.Contains(pending, "/embed/2") || strings.Contains(pending, "/r/2.dat") {
		t.Fatal("pending score must not offer viewer or download")
	}
	contains(t, e.do(http.MethodGet, "/s/3", nil).Body.String(), "has no replay")
	for _, p := range []string{"/s/abc", "/s/999", "/s/-1"} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s code = %d", p, rec.Code)
		}
	}
}
