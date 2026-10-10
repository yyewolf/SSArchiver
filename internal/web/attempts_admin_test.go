package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

const feedPath = "/identities/testplat/feeds/attempt"

func TestAdminAttemptsSwitch(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	row := "player-" + tess
	base := "/admin/players/" + tess + feedPath

	page := e.do(http.MethodGet, "/admin", nil, withCookie(c)).Body.String()
	contains(t, page, "Archive attempts", "several GB", `hx-post="`+base+`"`)

	on := e.do(http.MethodPost, base, url.Values{"enabled": {"true"}}, withCookie(c), htmx(row)).Body.String()
	contains(t, on, `id="player-`+tess+`"`, "Access is private", "History is private", "Make history public",
		"Open settings", `href="https://tp.example/settings"`, "Check again", `hx-post="`+base+`/check"`, "Stop archiving attempts")

	e.fp.Access["attempt/abc"] = model.AccessPublic
	e.fp.AccessTotal = 9
	ok := e.do(http.MethodPost, base+"/check", nil, withCookie(c), htmx(row)).Body.String()
	contains(t, ok, "Access granted", "attempts: public")
	if strings.Contains(ok, "Check again") {
		t.Fatal("the hint goes away once access is public")
	}

	off := e.do(http.MethodPost, base, url.Values{"enabled": {"false"}}, withCookie(c), htmx(row)).Body.String()
	contains(t, off, "Stopped archiving attempts", "Archive attempts")
	contains(t, e.do(http.MethodGet, "/admin", nil, withCookie(c)).Body.String(), "TestPlat reported 9 attempts at the last check")

	bad := e.do(http.MethodPost, "/admin/players/"+tess+"/identities/testplat/feeds/score", url.Values{"enabled": {"true"}}, withCookie(c), htmx(row))
	if bad.Header().Get("HX-Reswap") != "none" {
		t.Fatal("a refused switch must not swap the row")
	}
	contains(t, bad.Body.String(), "Could not change feed")
}

func TestRateLimitedAccessCheck(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	row := "player-" + tess
	base := "/admin/players/" + tess + feedPath
	e.fp.ProbeErr = fmt.Errorf("%w: slow down", platform.ErrRateLimited)

	on := e.do(http.MethodPost, base, url.Values{"enabled": {"true"}}, withCookie(c), htmx(row)).Body.String()
	contains(t, on, `id="player-`+tess+`"`, "attempts: checking access", "Check again later", `data-templ-type="warning"`)
	if strings.Contains(on, "Archiving attempts") {
		t.Fatal("a throttled probe must not let the switch toast success")
	}

	check := e.do(http.MethodPost, base+"/check", nil, withCookie(c), htmx(row)).Body.String()
	contains(t, check, `id="player-`+tess+`"`, "Check again later", `data-templ-type="warning"`)
	if strings.Contains(check, "Access granted") {
		t.Fatal("a throttled check must not toast success")
	}
	rec := e.do(http.MethodPost, base+"/check?from=sync", nil, withCookie(c), htmx(""))
	if rec.Header().Get("HX-Reswap") != "none" {
		t.Fatal("from the sync page, Check again only toasts (the live panel refreshes itself)")
	}
	contains(t, rec.Body.String(), "Check again later", `data-templ-type="warning"`)

	e.fp.ProbeErr = nil
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	contains(t, e.do(http.MethodPost, base+"/check", nil, withCookie(c), htmx(row)).Body.String(), "Access is private")
}

func TestSyncShowsPrivateFeed(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	ctx := context.Background()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	k := service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindAttempt}
	_, _ = e.svc.SetFeedEnabled(ctx, k, true)
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	_, _ = e.svc.CheckFeedAccess(ctx, k)

	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	check := "/admin/players/" + tess + feedPath + "/check?from=sync"
	contains(t, body, "TestPlat · attempts", "Private", "History is private", `hx-post="`+check+`"`)
	rec := e.do(http.MethodPost, check, nil, withCookie(c), htmx(""))
	if rec.Header().Get("HX-Reswap") != "none" {
		t.Fatal("from the sync page, Check again only toasts (the live panel refreshes itself)")
	}
	contains(t, rec.Body.String(), "Access is private")
}

func TestHintNeverOnPublicPages(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	ctx := context.Background()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	k := service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindAttempt}
	_, _ = e.svc.SetFeedEnabled(ctx, k, true)
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	_, _ = e.svc.CheckFeedAccess(ctx, k)
	for _, p := range []string{"/", "/p/" + tess} {
		body := e.do(http.MethodGet, p, nil).Body.String()
		if strings.Contains(body, "History is private") || strings.Contains(body, "Check again") {
			t.Fatalf("%s shows the admin hint", p)
		}
	}
}
