// Package web serves the HTML UI (templ + htmx).
package web

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/a-h/templ"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/buildinfo"
	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/viewer"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
	"github.com/yyewolf/ssarchiver/internal/web/static"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

type StatusSource interface {
	Status() archiver.Status
}

type Deps struct {
	Service *service.Service
	Status  StatusSource
	Viewer  *viewer.Handler
	Config  config.Config
}

type Handler struct {
	svc       *service.Service
	status    StatusSource
	viewer    *viewer.Handler
	cfg       config.Config
	logins    *loginLimiter
	setupDone atomic.Bool
}

func New(d Deps) *Handler {
	return &Handler{svc: d.Service, status: d.Status, viewer: d.Viewer, cfg: d.Config, logins: newLoginLimiter()}
}

// Routes registers every UI route. Later tasks add lines here.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /static/", static.Handler())
	mux.Handle("/viewer/", h.viewer)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /{$}", h.home)
	mux.HandleFunc("GET /setup", h.setupForm)
	mux.HandleFunc("POST /setup", h.setupSubmit)
	mux.HandleFunc("GET /login", h.loginForm)
	mux.HandleFunc("POST /login", h.loginSubmit)
	mux.HandleFunc("POST /logout", h.logout)
	mux.HandleFunc("GET /p/{id}", h.player)
	mux.HandleFunc("GET /stream", h.stream)
	mux.HandleFunc("GET /p/{id}/map", h.playerMap)
	mux.HandleFunc("GET /p/{slug}/{externalID}", h.accountLink)
	mux.HandleFunc("GET /s/{id}", h.score)
	mux.HandleFunc("GET /s/{slug}/{externalID}", h.platformScore(model.KindScore))
	mux.HandleFunc("GET /s/{slug}/attempt/{externalID}", h.platformScore(model.KindAttempt))
	mux.HandleFunc("GET /r/{file}", h.replayFile)
	mux.HandleFunc("GET /r/{slug}/{file}", h.platformReplay(model.KindScore))
	mux.HandleFunc("GET /r/{slug}/attempt/{file}", h.platformReplay(model.KindAttempt))
	mux.HandleFunc("OPTIONS /r/{file}", h.replayPreflight)
	mux.HandleFunc("OPTIONS /r/{slug}/{file}", h.replayPreflight)
	mux.HandleFunc("OPTIONS /r/{slug}/attempt/{file}", h.replayPreflight)
	mux.HandleFunc("GET /embed/{id}", h.embed)
	mux.HandleFunc("GET /embed/{slug}/{externalID}", h.platformEmbed(model.KindScore))
	mux.HandleFunc("GET /embed/{slug}/attempt/{externalID}", h.platformEmbed(model.KindAttempt))
	mux.HandleFunc("GET /admin", h.requireAdmin(h.adminPlayers))
	mux.HandleFunc("GET /admin/sync", h.requireAdmin(h.syncPage))
	mux.HandleFunc("GET /admin/sync/stream", h.requireAdmin(h.syncStream))
	mux.HandleFunc("GET /admin/sync/live", h.requireAdmin(h.syncLive))
	mux.HandleFunc("GET /admin/sync/events", h.requireAdmin(h.syncEvents))
	mux.HandleFunc("GET /admin/sync/failed", h.requireAdmin(h.syncFailed))
	mux.HandleFunc("POST /admin/sync/pause", h.requireAdmin(h.setPaused(true)))
	mux.HandleFunc("POST /admin/sync/resume", h.requireAdmin(h.setPaused(false)))
	mux.HandleFunc("POST /admin/sync/retry", h.requireAdmin(h.syncRetry))
	mux.HandleFunc("POST /admin/players/lookup", h.requireAdmin(h.lookupPlayer))
	mux.HandleFunc("POST /admin/players", h.requireAdmin(h.addPlayer))
	mux.HandleFunc("POST /admin/players/{id}/enabled", h.requireAdmin(h.setPlayerEnabled))
	mux.HandleFunc("POST /admin/players/{id}/poll", h.requireAdmin(h.pollPlayer))
	mux.HandleFunc("POST /admin/players/{id}/delete", h.requireAdmin(h.deletePlayer))
	mux.HandleFunc("POST /admin/players/{id}/identities", h.requireAdmin(h.linkIdentity))
	mux.HandleFunc("POST /admin/players/{id}/identities/{platform}/enabled", h.requireAdmin(h.setIdentityEnabled))
	mux.HandleFunc("POST /admin/players/{id}/identities/{platform}/delete", h.requireAdmin(h.unlinkIdentity))
	mux.HandleFunc("POST /admin/players/{id}/identities/{platform}/feeds/{kind}", h.requireAdmin(h.setFeedEnabled))
	mux.HandleFunc("POST /admin/players/{id}/identities/{platform}/feeds/{kind}/check", h.requireAdmin(h.checkFeedAccess))
	mux.HandleFunc("POST /admin/players/{id}/merge", h.requireAdmin(h.mergePlayer))
	mux.HandleFunc("GET /admin/settings", h.requireAdmin(h.settingsPage))
	mux.HandleFunc("POST /admin/settings", h.requireAdmin(h.saveSettings))
	mux.HandleFunc("POST /admin/settings/viewer", h.requireAdmin(h.saveViewerSettings))
	mux.HandleFunc("POST /admin/password", h.requireAdmin(h.changePassword))
}

// Middleware wraps the whole mux (UI and API).
func (h *Handler) Middleware(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	if h.cfg.BaseURL != "" {
		if err := cop.AddTrustedOrigin(h.cfg.BaseURL); err != nil {
			slog.Warn("invalid trusted origin", "url", h.cfg.BaseURL, "err", err)
		}
	}
	hd := cop.Handler(next)
	hd = h.requireSetup(hd)
	hd = h.loadSession(hd)
	hd = h.withPlatforms(hd)
	hd = httpx.BaseURL(h.cfg.BaseURL, h.cfg.TrustProxy)(hd)
	hd = httpx.SecurityHeaders(h.svc.Platforms().ImageHosts()...)(hd)
	hd = httpx.Logging(hd)
	return httpx.Recover(hd)
}

// withPlatforms gives every component the registry (views.ScoreURL & co).
func (h *Handler) withPlatforms(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(views.WithPlatforms(r.Context(), h.svc.Platforms())))
	})
}

func (h *Handler) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(httpx.SessionCookie); err == nil && c.Value != "" {
			if u, err := h.svc.UserForSession(r.Context(), c.Value); err == nil {
				r = r.WithContext(httpx.WithUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func exemptFromSetup(path string) bool {
	return path == "/setup" || path == "/healthz" || strings.HasPrefix(path, "/static/")
}

func (h *Handler) requireSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.setupDone.Load() || exemptFromSetup(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		need, err := h.svc.NeedsSetup(r.Context())
		if err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if !need {
			h.setupDone.Store(true)
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"title":"Service Unavailable","status":503,"detail":"setup required: open /setup in a browser"}`))
			return
		}
		redirect(w, r, "/setup")
	})
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// redirect sends a 303, or an HX-Redirect for htmx requests.
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusOK)
		return
	}
	// #nosec G710 -- callers pass constants or paths validated by safeNext
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		slog.Error("render failed", "path", r.URL.Path, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (h *Handler) page(r *http.Request, title string) views.Page {
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		st = service.DefaultSettings
	}
	return views.Page{Title: title, Instance: st.InstanceTitle, Path: r.URL.Path, Version: buildinfo.Version, User: httpx.UserFrom(r.Context())}
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, msg string) {
	render(w, r, http.StatusNotFound, views.NotFound(h.page(r, "Not found"), msg))
}

func (h *Handler) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "path", r.URL.Path, "err", err)
	if isHTMX(r) {
		h.toastOnly(w, r, toast.TypeError, "Something went wrong", "The error was logged.")
		return
	}
	render(w, r, http.StatusInternalServerError, views.ServerError(h.page(r, "Error")))
}

// toastOnly answers an htmx request with just a toast and no swap.
func (h *Handler) toastOnly(w http.ResponseWriter, r *http.Request, t toast.Type, title, desc string) {
	w.Header().Set("HX-Reswap", "none")
	render(w, r, http.StatusOK, views.ToastOOB(t, title, desc))
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

func isNotFound(err error) bool { return errors.Is(err, service.ErrNotFound) }
