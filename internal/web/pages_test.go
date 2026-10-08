package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/httpx"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/healthz", nil)
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestRedirectsToSetupUntilDone(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	api := e.do(http.MethodGet, "/api/v1/players", nil)
	if api.Code != http.StatusServiceUnavailable {
		t.Fatalf("api before setup = %d", api.Code)
	}
	e.setup()
	if rec := e.do(http.MethodGet, "/", nil); rec.Code != 200 {
		t.Fatalf("after setup code = %d", rec.Code)
	}
}

func TestHomeListsPlayersWithSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	e.setup()
	empty := e.do(http.MethodGet, "/", nil)
	contains(t, empty.Body.String(), "No players yet")
	e.seed()
	rec := e.do(http.MethodGet, "/", nil)
	body := rec.Body.String()
	contains(t, body, "Alice", `href="/p/1001"`, "/static/css/app.css?v=", "data-theme-toggle")
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "'nonce-") {
		t.Fatalf("csp = %q", csp)
	}
	nonce := strings.SplitN(strings.SplitN(csp, "'nonce-", 2)[1], "'", 2)[0]
	contains(t, body, `nonce="`+nonce+`"`) // components.Scripts() carries the nonce
}

func TestStaticCaching(t *testing.T) {
	e := newEnv(t)
	versioned := e.do(http.MethodGet, "/static/js/app.js?v=abc", nil)
	if versioned.Code != 200 || !strings.Contains(versioned.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("versioned = %d %q", versioned.Code, versioned.Header().Get("Cache-Control"))
	}
	if dir := e.do(http.MethodGet, "/static/js/", nil); dir.Code != 404 {
		t.Fatalf("directory listing must be disabled, got %d", dir.Code)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	e := newEnv(t)
	e.setup()
	rec := e.do(http.MethodPost, "/logout", url.Values{}, withHeader("Sec-Fetch-Site", "cross-site"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", rec.Code)
	}
}

func TestSessionCookieAuthenticates(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/", nil, withCookie(e.login()))
	contains(t, rec.Body.String(), "Log out", "/admin")
	bad := e.do(http.MethodGet, "/", nil, withCookie(&http.Cookie{Name: httpx.SessionCookie, Value: "bogus"}))
	if strings.Contains(bad.Body.String(), "Log out") {
		t.Fatal("invalid session cookie must not authenticate")
	}
}

func TestHTMXRedirectUsesHXRedirect(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/", nil, htmx(""))
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/setup" {
		t.Fatalf("code=%d hx-redirect=%q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
}
