package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/httpx"
)

func sessionCookie(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == httpx.SessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func TestSetupFlow(t *testing.T) {
	e := newEnv(t)
	contains(t, e.do(http.MethodGet, "/setup", nil).Body.String(), "Create admin account")

	bad := e.do(http.MethodPost, "/setup", url.Values{"username": {"admin"}, "password": {adminPW}, "confirm": {"different password"}})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch code = %d", bad.Code)
	}
	contains(t, bad.Body.String(), "Passwords do not match")

	weak := e.do(http.MethodPost, "/setup", url.Values{"username": {"admin"}, "password": {"short"}, "confirm": {"short"}})
	if weak.Code != http.StatusUnprocessableEntity {
		t.Fatalf("weak code = %d", weak.Code)
	}
	contains(t, weak.Body.String(), "at least 10 characters")

	ok := e.do(http.MethodPost, "/setup", url.Values{"username": {"admin"}, "password": {adminPW}, "confirm": {adminPW}})
	if ok.Code != http.StatusSeeOther || ok.Header().Get("Location") != "/admin" {
		t.Fatalf("setup code=%d location=%q", ok.Code, ok.Header().Get("Location"))
	}
	c := sessionCookie(t, ok.Result())
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie flags = %+v", c)
	}
	if u, err := e.svc.UserForSession(context.Background(), c.Value); err != nil || u.Username != "admin" {
		t.Fatalf("session invalid: %v", err)
	}
	if again := e.do(http.MethodGet, "/setup", nil); again.Code != http.StatusNotFound {
		t.Fatalf("setup after completion = %d, want 404", again.Code)
	}
	if again := e.do(http.MethodPost, "/setup", url.Values{"username": {"x"}, "password": {adminPW}, "confirm": {adminPW}}); again.Code != http.StatusNotFound {
		t.Fatalf("second setup POST = %d, want 404", again.Code)
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	contains(t, e.do(http.MethodGet, "/login?next=/p/1001", nil).Body.String(), `value="/p/1001"`)

	wrong := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"wrong password"}})
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password code = %d", wrong.Code)
	}
	contains(t, wrong.Body.String(), "Invalid username or password")

	ok := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPW}, "next": {"/p/1001"}})
	if ok.Code != http.StatusSeeOther || ok.Header().Get("Location") != "/p/1001" {
		t.Fatalf("login code=%d location=%q", ok.Code, ok.Header().Get("Location"))
	}
	evil := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPW}, "next": {"//evil.example"}})
	if evil.Header().Get("Location") != "/admin" {
		t.Fatalf("open redirect: %q", evil.Header().Get("Location"))
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for i := range 5 {
		if rec := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"nope nope nope"}}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d code = %d", i+1, rec.Code)
		}
	}
	rec := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPW}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt code = %d, want 429", rec.Code)
	}
	contains(t, rec.Body.String(), "Too many attempts")
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	rec := e.do(http.MethodPost, "/logout", url.Values{}, withCookie(c))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("logout code=%d", rec.Code)
	}
	cleared := false
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == httpx.SessionCookie && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout must clear the cookie")
	}
	if _, err := e.svc.UserForSession(context.Background(), c.Value); err == nil {
		t.Fatal("logout must delete the session")
	}
	home := e.do(http.MethodGet, "/", nil, withCookie(c))
	if strings.Contains(home.Body.String(), "Log out") {
		t.Fatal("stale cookie still treated as logged in")
	}
}
