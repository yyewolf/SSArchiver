package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestAdminRequiresLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	rec := e.do(http.MethodGet, "/admin", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Fadmin" {
		t.Fatalf("code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	hx := e.do(http.MethodPost, "/admin/players", url.Values{"ref": {"1001"}}, htmx(""))
	if hx.Code != http.StatusUnauthorized || !strings.HasPrefix(hx.Header().Get("HX-Redirect"), "/login") {
		t.Fatalf("htmx code=%d redirect=%q", hx.Code, hx.Header().Get("HX-Redirect"))
	}
}

func TestLookupPlayer(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ok := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"https://scoresaber.com/u/1001?page=2"}}, withCookie(c), htmx("lookup-result"))
	contains(t, ok.Body.String(), "Alice", "Track player", `value="1001"`)
	bad := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"not a link"}}, withCookie(c), htmx("lookup-result"))
	contains(t, bad.Body.String(), "Enter a ScoreSaber player ID")
	missing := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"9999"}}, withCookie(c), htmx("lookup-result"))
	contains(t, missing.Body.String(), "No ScoreSaber player found")
	e.seed()
	tracked := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"1001"}}, withCookie(c), htmx("lookup-result"))
	contains(t, tracked.Body.String(), "Already tracked")
}

func TestAddPlayer(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	rec := e.do(http.MethodPost, "/admin/players", url.Values{"ref": {"1002"}}, withCookie(c), htmx("admin-players"))
	body := rec.Body.String()
	contains(t, body, `id="admin-players"`, "Bob", "data-templ-toast", "Now tracking Bob", `id="lookup-result" hx-swap-oob`)
	if _, err := e.svc.GetPlayer(context.Background(), "1002"); err != nil {
		t.Fatal("player not stored")
	}
	dup := e.do(http.MethodPost, "/admin/players", url.Values{"ref": {"1002"}}, withCookie(c), htmx("admin-players"))
	if dup.Header().Get("HX-Reswap") != "none" {
		t.Fatal("duplicate add must not swap the table")
	}
	contains(t, dup.Body.String(), "Already tracked")
}

func TestPlayerRowActions(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	e.seed()
	ctx := context.Background()
	_ = e.svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey("1001"))

	off := e.do(http.MethodPost, "/admin/players/1001/enabled", url.Values{"enabled": {"false"}}, withCookie(c), htmx("player-1001"))
	contains(t, off.Body.String(), `id="player-1001"`, "Disabled", "Tracking paused")
	if p, _ := e.svc.GetPlayer(ctx, "1001"); p.Enabled {
		t.Fatal("player still enabled")
	}

	poll := e.do(http.MethodPost, "/admin/players/1001/poll", url.Values{}, withCookie(c), htmx(""))
	contains(t, poll.Body.String(), "Poll queued")
	if f := testutil.ScoreFeed(t, e.svc, "1001"); f.LastPolledAt != nil {
		t.Fatal("poll not requested")
	}

	del := e.do(http.MethodPost, "/admin/players/1001/delete", url.Values{"delete_files": {"on"}}, withCookie(c), htmx("player-1001"))
	contains(t, del.Body.String(), "Stopped tracking Alice")
	if strings.Contains(del.Body.String(), `id="player-1001"`) {
		t.Fatal("deleted row must be replaced with nothing")
	}
	if _, err := e.svc.Store().Open("1001", 1); err == nil {
		t.Fatal("replay files not deleted")
	}
	missing := e.do(http.MethodPost, "/admin/players/1001/poll", url.Values{}, withCookie(c), htmx(""))
	contains(t, missing.Body.String(), "not found")
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ctx := context.Background()
	contains(t, e.do(http.MethodGet, "/admin/settings", nil, withCookie(c)).Body.String(), "Instance title", "Poll every", "Change password")

	bad := e.do(http.MethodPost, "/admin/settings", url.Values{"title": {""}, "poll_interval": {"10m0s"}}, withCookie(c), htmx("settings-general"))
	contains(t, bad.Body.String(), `id="settings-general"`, "Title must be 1-64 characters")

	ok := e.do(http.MethodPost, "/admin/settings", url.Values{"title": {"Oermer replays"}, "poll_interval": {"15m0s"}}, withCookie(c), htmx("settings-general"))
	contains(t, ok.Body.String(), "Settings saved")
	if st, _ := e.svc.Settings(ctx); st.InstanceTitle != "Oermer replays" || st.PollInterval != 15*time.Minute {
		t.Fatalf("settings = %+v", st)
	}
	contains(t, e.do(http.MethodGet, "/", nil).Body.String(), "<title>Oermer replays</title>")

	wrong := e.do(http.MethodPost, "/admin/password", url.Values{"current": {"nope nope nope"}, "new": {"brand new password"}, "confirm": {"brand new password"}}, withCookie(c), htmx("settings-password"))
	contains(t, wrong.Body.String(), "Current password is incorrect")
	mismatch := e.do(http.MethodPost, "/admin/password", url.Values{"current": {adminPW}, "new": {"brand new password"}, "confirm": {"other"}}, withCookie(c), htmx("settings-password"))
	contains(t, mismatch.Body.String(), "Passwords do not match")
	done := e.do(http.MethodPost, "/admin/password", url.Values{"current": {adminPW}, "new": {"brand new password"}, "confirm": {"brand new password"}}, withCookie(c), htmx("settings-password"))
	if done.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("password change must redirect to login, headers = %v", done.Header())
	}
	if _, err := e.svc.Login(ctx, "admin", "brand new password"); err != nil {
		t.Fatal("new password not active")
	}
}

func TestViewerSettingsCard(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ctx := context.Background()
	contains(t, e.do(http.MethodGet, "/admin/settings", nil, withCookie(c)).Body.String(), "Replay viewer", "Show headset", "Headset color", "Headset opacity")

	bad := e.do(http.MethodPost, "/admin/settings/viewer", url.Values{"show_headset": {"on"}, "headset_color": {"ff0080"}, "headset_alpha": {"0.5"}}, withCookie(c), htmx("settings-viewer"))
	contains(t, bad.Body.String(), `id="settings-viewer"`, "Headset color must be a hex color like #878787")

	badAlpha := e.do(http.MethodPost, "/admin/settings/viewer", url.Values{"headset_color": {"#ff0080"}, "headset_alpha": {"2"}}, withCookie(c), htmx("settings-viewer"))
	contains(t, badAlpha.Body.String(), `id="settings-viewer"`, "Headset opacity must be between 0 and 1")

	ok := e.do(http.MethodPost, "/admin/settings/viewer", url.Values{"show_headset": {"on"}, "headset_color": {"#Ff0080"}, "headset_alpha": {"0.5"}}, withCookie(c), htmx("settings-viewer"))
	contains(t, ok.Body.String(), "Viewer settings saved")
	if st, _ := e.svc.Settings(ctx); !st.ViewerShowHeadset || st.ViewerHeadsetColor != "#ff0080" || st.ViewerHeadsetAlpha != 0.5 {
		t.Fatalf("viewer settings = %+v", st)
	}

	// Saving general settings must not reset the viewer card.
	general := e.do(http.MethodPost, "/admin/settings", url.Values{"title": {"Oermer replays"}, "poll_interval": {"15m0s"}}, withCookie(c), htmx("settings-general"))
	contains(t, general.Body.String(), "Settings saved")
	if st, _ := e.svc.Settings(ctx); !st.ViewerShowHeadset || st.ViewerHeadsetColor != "#ff0080" {
		t.Fatalf("general save reset viewer settings: %+v", st)
	}
}
