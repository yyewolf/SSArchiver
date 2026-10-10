package web

import (
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

func setCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Content-Disposition, ETag")
}

// replayFile serves the legacy /r/{id}.dat (and its .bsor alias).
func (h *Handler) replayFile(w http.ResponseWriter, r *http.Request) {
	setCORS(w.Header())
	idStr, ok := strings.CutSuffix(r.PathValue("file"), ".dat")
	if !ok {
		idStr, ok = strings.CutSuffix(r.PathValue("file"), ".bsor")
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	sc, err := h.legacyPlay(r, idStr)
	h.serveReplay(w, r, sc, err)
}

// platformReplay serves /r/{slug}/{externalID}{ext} and /r/{slug}/attempt/….
// Open-replay platforms (BSOR) also answer their replay under a .bsor name:
// BeatLeader's viewer only loads replay links ending in .bsor.
func (h *Handler) platformReplay(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w.Header())
		p, ok := h.svc.Platforms().BySlug(r.PathValue("slug"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		file := r.PathValue("file")
		id, ok := strings.CutSuffix(file, p.ReplayExt)
		if !ok && p.BSOR {
			id, ok = strings.CutSuffix(file, ".bsor")
		}
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

// viewerCookie stores a visitor's replay-viewer choice for a year.
const viewerCookie = "ssa_viewer"

func (h *Handler) setViewerCookie(w http.ResponseWriter, r *http.Request, viewer string) {
	c := &http.Cookie{
		Name: viewerCookie, Value: viewer, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int((365 * 24 * time.Hour).Seconds()),
	}
	if httpx.IsHTTPS(r, h.cfg.BaseURL, h.cfg.TrustProxy) {
		c.Secure = true
	}
	http.SetCookie(w, c)
}

// pickViewer resolves a preferred viewer to one that can play the row:
// ArcViewer needs the bundled build, the BeatLeader viewer an open-replay
// platform; "" when neither can.
func pickViewer(v string, arcOK, blOK bool) string {
	switch v {
	case service.ViewerArcViewer:
		if arcOK {
			return service.ViewerArcViewer
		}
		if blOK {
			return service.ViewerBeatLeader
		}
	case service.ViewerBeatLeader:
		if blOK {
			return service.ViewerBeatLeader
		}
		if arcOK {
			return service.ViewerArcViewer
		}
	}
	return ""
}

// platformViewer is the viewer a platform's replays play in by default:
// BeatLeader's hosted viewer for its own rows, the bundled ArcViewer for the
// rest (score pages let visitors switch, see pickViewer).
func platformViewer(name string) string {
	if name == model.PlatformBeatLeader {
		return service.ViewerBeatLeader
	}
	return service.ViewerArcViewer
}

// embedViewer resolves the embed page's viewer: an explicit ?viewer= beats the
// row's platform default (embeds stay stateless; the score page carries the
// visitor choice in its iframe URL).
func (h *Handler) embedViewer(q url.Values, platform string, arcOK, blOK bool) string {
	if v := pickViewer(q.Get("viewer"), arcOK, blOK); v != "" {
		return v
	}
	return pickViewer(platformViewer(platform), arcOK, blOK)
}

// embedPage is the iframe-able wrapper around the replay viewers.
func (h *Handler) embedPage(w http.ResponseWriter, r *http.Request, sc *model.Score, err error) {
	w.Header().Set("Content-Security-Policy", httpx.EmbedCSP)
	if err != nil || sc.ReplayState != model.ReplayArchived || !views.ViewerPlays(sc) {
		render(w, r, http.StatusNotFound, views.EmbedUnavailable())
		return
	}
	q := r.URL.Query()
	ctx := r.Context()
	st, serr := h.svc.Settings(ctx)
	if serr != nil {
		st = service.DefaultSettings
	}
	arcOK, blOK := h.viewer.Available(), views.BeatLeaderPlays(ctx, sc)
	viewer := h.embedViewer(q, sc.Platform, arcOK, blOK)
	if viewer == "" {
		render(w, r, http.StatusNotFound, views.EmbedUnavailable())
		return
	}
	var src string
	if viewer == service.ViewerBeatLeader {
		src = views.BeatLeaderSrc(ctx, httpx.BaseURLFrom(ctx), sc, q.Get("autoplay") == "1", q.Get("loop") == "1")
	} else {
		src = views.ViewerSrc(ctx, httpx.BaseURLFrom(ctx), sc, q.Get("autoplay") == "1", q.Get("loop") == "1", q.Get("ui") == "0", st)
	}
	render(w, r, http.StatusOK, views.Embed(views.SongTitle(sc)+" · "+views.PlayerName(sc), src))
}
