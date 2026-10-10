package web

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

func setCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Content-Disposition, ETag")
}

// replayFile serves the legacy /r/{id}.dat.
func (h *Handler) replayFile(w http.ResponseWriter, r *http.Request) {
	setCORS(w.Header())
	idStr, ok := strings.CutSuffix(r.PathValue("file"), ".dat")
	if !ok {
		http.NotFound(w, r)
		return
	}
	sc, err := h.legacyPlay(r, idStr)
	h.serveReplay(w, r, sc, err)
}

// platformReplay serves /r/{slug}/{externalID}{ext} and /r/{slug}/attempt/….
func (h *Handler) platformReplay(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w.Header())
		p, ok := h.svc.Platforms().BySlug(r.PathValue("slug"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		id, ok := strings.CutSuffix(r.PathValue("file"), p.ReplayExt)
		if !ok || id == "" {
			http.NotFound(w, r)
			return
		}
		sc, err := h.svc.GetPlay(r.Context(), p.Name, kind, id)
		h.serveReplay(w, r, sc, err)
	}
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// serveReplay streams an archived replay: CORS *, Range, ETag (sha256),
// immutable caching, download as {externalID}{ext}.
func (h *Handler) serveReplay(w http.ResponseWriter, r *http.Request, sc *model.Score, err error) {
	if err != nil || sc.ReplayState != model.ReplayArchived {
		http.NotFound(w, r)
		return
	}
	f, err := h.svc.OpenReplay(sc)
	if err != nil {
		slog.Error("archived replay file missing", "score", sc.ID, "err", err)
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	name := unsafeFileChars.ReplaceAllString(sc.ExternalID+views.ReplayExt(r.Context(), sc), "_")
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	hd.Set("ETag", `"`+sc.ReplaySHA256+`"`)
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (h *Handler) replayPreflight(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	setCORS(hd)
	hd.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	hd.Set("Access-Control-Allow-Headers", "Range")
	hd.Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// embed serves the legacy /embed/{id}.
func (h *Handler) embed(w http.ResponseWriter, r *http.Request) {
	sc, err := h.legacyPlay(r, r.PathValue("id"))
	h.embedPage(w, r, sc, err)
}

// platformEmbed serves /embed/{slug}/{externalID} and /embed/{slug}/attempt/….
func (h *Handler) platformEmbed(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, err := h.slugPlay(r, kind, r.PathValue("externalID"))
		h.embedPage(w, r, sc, err)
	}
}

// embedPage is the iframe-able wrapper around the same-origin viewer.
func (h *Handler) embedPage(w http.ResponseWriter, r *http.Request, sc *model.Score, err error) {
	w.Header().Set("Content-Security-Policy", httpx.EmbedCSP)
	if err != nil || !h.viewer.Available() || sc.ReplayState != model.ReplayArchived {
		render(w, r, http.StatusNotFound, views.EmbedUnavailable())
		return
	}
	q := r.URL.Query()
	st, serr := h.svc.Settings(r.Context())
	if serr != nil {
		st = service.DefaultSettings
	}
	src := views.ViewerSrc(r.Context(), httpx.BaseURLFrom(r.Context()), sc, q.Get("autoplay") == "1", q.Get("loop") == "1", q.Get("ui") == "0", st)
	render(w, r, http.StatusOK, views.Embed(views.SongTitle(sc)+" · "+views.PlayerName(sc), src))
}
