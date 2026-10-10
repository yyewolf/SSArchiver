package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

const maxFormBytes = 64 << 10

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func safeNext(s string) string {
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "/\\") {
		return s
	}
	return "/admin"
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	c := &http.Cookie{
		Name: httpx.SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(service.SessionTTL / time.Second),
	}
	if httpx.IsHTTPS(r, h.cfg.BaseURL, h.cfg.TrustProxy) {
		c.Secure = true
	}
	http.SetCookie(w, c)
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{
		Name: httpx.SessionCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	}
	if httpx.IsHTTPS(r, h.cfg.BaseURL, h.cfg.TrustProxy) {
		c.Secure = true
	}
	http.SetCookie(w, c)
}

func (h *Handler) setupForm(w http.ResponseWriter, r *http.Request) {
	if need, err := h.svc.NeedsSetup(r.Context()); err != nil || !need {
		h.notFound(w, r, "Setup has already been completed.")
		return
	}
	render(w, r, http.StatusOK, views.Setup(h.page(r, "Set up"), views.AuthForm{}))
}

func (h *Handler) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	username, pw := r.PostFormValue("username"), r.PostFormValue("password")
	form := views.AuthForm{Username: username}
	fail := func(msg string) {
		form.Error = msg
		render(w, r, http.StatusUnprocessableEntity, views.Setup(h.page(r, "Set up"), form))
	}
	if pw != r.PostFormValue("confirm") {
		fail("Passwords do not match.")
		return
	}
	_, err := h.svc.Setup(r.Context(), username, pw)
	switch {
	case errors.Is(err, service.ErrAlreadySetup):
		h.notFound(w, r, "Setup has already been completed.")
		return
	case errors.Is(err, service.ErrWeakPassword), errors.Is(err, service.ErrInvalidUsername):
		fail(strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + ".")
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	h.setupDone.Store(true)
	token, err := h.svc.Login(r.Context(), username, pw)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.setSessionCookie(w, r, token)
	redirect(w, r, "/admin")
}

func (h *Handler) loginForm(w http.ResponseWriter, r *http.Request) {
	if httpx.UserFrom(r.Context()) != nil {
		redirect(w, r, safeNext(r.URL.Query().Get("next")))
		return
	}
	render(w, r, http.StatusOK, views.Login(h.page(r, "Log in"), views.AuthForm{Next: safeNext(r.URL.Query().Get("next"))}))
}

func (h *Handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	form := views.AuthForm{Username: r.PostFormValue("username"), Next: safeNext(r.PostFormValue("next"))}
	if !h.logins.Allow(httpx.ClientIP(r, h.cfg.TrustProxy), h.svc.Now()) {
		form.Error = "Too many attempts. Try again in a minute."
		render(w, r, http.StatusTooManyRequests, views.Login(h.page(r, "Log in"), form))
		return
	}
	token, err := h.svc.Login(r.Context(), form.Username, r.PostFormValue("password"))
	if errors.Is(err, service.ErrInvalidCredentials) {
		form.Error = "Invalid username or password."
		render(w, r, http.StatusUnauthorized, views.Login(h.page(r, "Log in"), form))
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.setSessionCookie(w, r, token)
	redirect(w, r, form.Next)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(httpx.SessionCookie); err == nil && c.Value != "" {
		_ = h.svc.Logout(r.Context(), c.Value)
	}
	h.clearSessionCookie(w, r)
	redirect(w, r, "/")
}

// requireAdmin guards admin handlers.
func (h *Handler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if httpx.UserFrom(r.Context()) != nil {
			next(w, r)
			return
		}
		// An EventSource cannot use a login redirect: answer 401 directly.
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		target := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
		if isHTMX(r) {
			w.Header().Set("HX-Redirect", target)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}
