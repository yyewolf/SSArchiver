package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestAdminFeedDownloadPause(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	row := "player-" + tess
	base := "/admin/players/" + tess + "/identities/testplat/feeds/score/download"
	feedState := func() bool {
		sum, err := e.svc.GetPlayerSummary(context.Background(), tess)
		if err != nil {
			t.Fatal(err)
		}
		return sum.Identities[0].Feed(model.KindScore).DownloadEnabled
	}

	page := e.do(http.MethodGet, "/admin", nil, withCookie(c)).Body.String()
	contains(t, page, "Pause replay downloads", `hx-post="`+base+`"`)

	off := e.do(http.MethodPost, base, url.Values{"enabled": {"false"}}, withCookie(c), htmx(row)).Body.String()
	contains(t, off, "Paused replay downloads", "Resume replay downloads")
	if feedState() {
		t.Fatal("the pause must persist")
	}

	// The sync page marks paused feeds in the queue.
	sync := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	if !strings.Contains(sync, "(downloads paused)") {
		t.Fatalf("sync page misses the paused hint:\n%.400s", sync)
	}

	on := e.do(http.MethodPost, base, url.Values{"enabled": {"true"}}, withCookie(c), htmx(row)).Body.String()
	contains(t, on, "Resumed replay downloads", "Pause replay downloads")
	if !feedState() {
		t.Fatal("the resume must persist")
	}
	if strings.Contains(e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String(), "(downloads paused)") {
		t.Fatal("the paused hint must go away after resuming")
	}

	if rec := e.do(http.MethodPost, base, url.Values{"enabled": {"false"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("anonymous toggle = %d, want 303", rec.Code)
	}
}
