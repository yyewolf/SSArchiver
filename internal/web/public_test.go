package web_test

import (
	"net/http"
	"strings"
	"testing"
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
