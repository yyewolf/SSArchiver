package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestAdminLinkUnlinkIdentity(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	a := e.seed()
	ctx := context.Background()
	row := "player-" + a

	link := e.do(http.MethodPost, "/admin/players/"+a+"/identities", url.Values{"platform": {"testplat"}, "ref": {"https://tp.example/u/abc"}}, withCookie(c), htmx(row))
	contains(t, link.Body.String(), `id="player-`+a+`"`, "TestPlat", "Account linked")
	if sum, _ := e.svc.GetPlayerSummary(ctx, a); len(sum.Identities) != 2 {
		t.Fatalf("identities = %d", len(sum.Identities))
	}

	again := e.do(http.MethodPost, "/admin/players/"+a+"/identities", url.Values{"platform": {"testplat"}, "ref": {"def"}}, withCookie(c), htmx(row))
	if again.Header().Get("HX-Reswap") != "none" {
		t.Fatal("a refused link must not swap the row")
	}
	contains(t, again.Body.String(), "Could not link account", "already has an account on that platform")

	bob := testutil.AddPlayer(t, e.svc, "1002")
	elsewhere := e.do(http.MethodPost, "/admin/players/"+bob+"/identities", url.Values{"platform": {"testplat"}, "ref": {"abc"}}, withCookie(c), htmx("player-"+bob))
	contains(t, elsewhere.Body.String(), "Already tracked", "already tracked as Alice", "merge the two players")

	pause := e.do(http.MethodPost, "/admin/players/"+a+"/identities/testplat/enabled", url.Values{"enabled": {"false"}}, withCookie(c), htmx(row))
	contains(t, pause.Body.String(), "Account paused", "Resume this account")
	if sum, _ := e.svc.GetPlayerSummary(ctx, a); sum.Identities[1].Enabled {
		t.Fatal("account not paused")
	}

	unlink := e.do(http.MethodPost, "/admin/players/"+a+"/identities/testplat/delete", url.Values{"delete_files": {"on"}}, withCookie(c), htmx(row))
	contains(t, unlink.Body.String(), "Account unlinked")
	if sum, _ := e.svc.GetPlayerSummary(ctx, a); len(sum.Identities) != 1 {
		t.Fatal("account not unlinked")
	}
	last := e.do(http.MethodPost, "/admin/players/"+a+"/identities/scoresaber/delete", url.Values{}, withCookie(c), htmx(row))
	contains(t, last.Body.String(), "Could not unlink account", "keeps at least one account")
	missing := e.do(http.MethodPost, "/admin/players/"+a+"/identities/testplat/enabled", url.Values{"enabled": {"true"}}, withCookie(c), htmx(row))
	contains(t, missing.Body.String(), "Account not found")
}

func TestAdminMergePlayers(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	a := e.seed()
	tess := e.seedTP()
	bob := testutil.AddPlayer(t, e.svc, "1002")

	page := e.do(http.MethodGet, "/admin", nil, withCookie(c)).Body.String()
	contains(t, page, "Detect from URL", `value="testplat"`, `hx-post="/admin/players/`+tess+`/merge"`,
		`hx-post="/admin/players/`+a+`/identities"`, "Link account")
	// Tess can go into Alice or Bob; Alice and Bob share ScoreSaber, so Alice's only target is Tess.
	aliceRow := rowOf(t, page, a)
	if !strings.Contains(aliceRow, `value="`+tess+`"`) || strings.Contains(aliceRow, `<option value="`+bob+`"`) {
		t.Fatalf("Alice's merge targets:\n%s", aliceRow)
	}

	conflict := e.do(http.MethodPost, "/admin/players/"+bob+"/merge", url.Values{"into": {a}}, withCookie(c), htmx("admin-players"))
	contains(t, conflict.Body.String(), "Could not merge", "same platform")

	ok := e.do(http.MethodPost, "/admin/players/"+tess+"/merge", url.Values{"into": {a}}, withCookie(c), htmx("admin-players"))
	body := ok.Body.String()
	contains(t, body, `id="admin-players"`, "Merged Tess into Alice")
	if strings.Contains(body, `id="player-`+tess+`"`) {
		t.Fatal("the merged-away row must disappear")
	}
	if _, err := e.svc.GetPlayer(context.Background(), tess); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("merge not applied")
	}
}

func TestLookupPlayerOnPlatform(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	bare := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"abc"}, "platform": {"testplat"}}, withCookie(c), htmx("lookup-result"))
	contains(t, bare.Body.String(), "Tess", `value="testplat"`, "Track player")
	byURL := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"https://tp.example/u/abc"}}, withCookie(c), htmx("lookup-result"))
	contains(t, byURL.Body.String(), "Tess", `value="testplat"`)
	add := e.do(http.MethodPost, "/admin/players", url.Values{"ref": {"abc"}, "platform": {"testplat"}}, withCookie(c), htmx("admin-players"))
	contains(t, add.Body.String(), "Now tracking Tess")
}

// rowOf returns the <tr> of a player in the admin table.
func rowOf(t *testing.T, page, playerID string) string {
	t.Helper()
	start := strings.Index(page, `id="player-`+playerID+`"`)
	if start < 0 {
		t.Fatalf("no row for %s", playerID)
	}
	end := strings.Index(page[start:], "</tr>")
	return page[start : start+end]
}
