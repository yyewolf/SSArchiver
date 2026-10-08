package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/p/1001":              "/p/1001",
		"/admin":               "/admin",
		"/a b?q=1":             "/a b?q=1",
		"":                     "/admin",
		"//evil.example/x":     "/admin",
		`/\evil`:               "/admin",
		"https://evil.example": "/admin",
		"p/1001":               "/admin",
		"javascript:alert(1)":  "/admin",
	} {
		if got := safeNext(in); got != want {
			t.Fatalf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequireAdmin(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	h := New(Deps{Service: svc, Config: config.Config{BaseURL: "https://replays.example.com"}})
	guarded := h.requireAdmin(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	newReq := func(ctx context.Context) *http.Request {
		return httptest.NewRequestWithContext(ctx, http.MethodGet, "/admin/settings", nil)
	}
	target := "/login?next=" + url.QueryEscape("/admin/settings")

	rec := httptest.NewRecorder()
	guarded(rec, newReq(context.Background()))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != target {
		t.Fatalf("anonymous = %d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	hx := newReq(context.Background())
	hx.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	guarded(rec, hx)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != target {
		t.Fatalf("htmx anonymous = %d hx-redirect=%q", rec.Code, rec.Header().Get("HX-Redirect"))
	}

	admin := newReq(httpx.WithUser(context.Background(), &model.User{Username: "admin"}))
	rec = httptest.NewRecorder()
	guarded(rec, admin)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("authenticated = %d, want pass-through", rec.Code)
	}
}
