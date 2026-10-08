package web

import (
	"log/slog"
	"net/http"
	"strconv"
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

// replayFile serves /r/{id}.dat for archived replays only.
func (h *Handler) replayFile(w http.ResponseWriter, r *http.Request) {
	setCORS(w.Header())
	idStr, ok := strings.CutSuffix(r.PathValue("file"), ".dat")
	id, okID := parseID(idStr)
	if !ok || !okID {
		http.NotFound(w, r)
		return
	}
	sc, err := h.svc.GetScore(r.Context(), id)
	if err != nil || sc.ReplayState != model.ReplayArchived {
		http.NotFound(w, r)
		return
	}
	f, err := h.svc.Store().Open(sc.PlayerID, sc.ID)
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
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", `attachment; filename="`+strconv.FormatInt(sc.ID, 10)+`.dat"`)
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

// embed serves the iframe-able wrapper around the same-origin viewer.
func (h *Handler) embed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", httpx.EmbedCSP)
	id, ok := parseID(r.PathValue("id"))
	if !ok || !h.viewer.Available() {
		render(w, r, http.StatusNotFound, views.EmbedUnavailable())
		return
	}
	sc, err := h.svc.GetScore(r.Context(), id)
	if err != nil || sc.ReplayState != model.ReplayArchived {
		render(w, r, http.StatusNotFound, views.EmbedUnavailable())
		return
	}
	q := r.URL.Query()
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		st = service.DefaultSettings
	}
	src := views.ViewerSrc(httpx.BaseURLFrom(r.Context()), id, q.Get("autoplay") == "1", q.Get("loop") == "1", q.Get("ui") == "0", st)
	render(w, r, http.StatusOK, views.Embed(views.SongTitle(sc)+" · "+views.PlayerName(sc), src))
}
