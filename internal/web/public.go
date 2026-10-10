package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

// accountLink resolves a readable account URL (/p/ss/{id}) to the player's
// page; it keeps working through merges (spec §6.1).
func (h *Handler) accountLink(w http.ResponseWriter, r *http.Request) {
	p, ok := h.svc.Platforms().BySlug(r.PathValue("slug"))
	if !ok {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	pl, err := h.svc.PlayerByIdentity(r.Context(), p.Name, r.PathValue("externalID"))
	if isNotFound(err) {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/p/"+url.PathEscape(pl.ID), http.StatusMovedPermanently)
}

func (h *Handler) player(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	sum, err := h.svc.GetPlayerSummary(ctx, id)
	if isNotFound(err) {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	pl := &sum.Player
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	f := service.ScoreFilter{PlayerID: id, Search: q.Get("q"), RankedOnly: q.Get("ranked") == "1", Page: page, PerPage: 50}
	if s := q.Get("state"); s == service.FilterWithReplay || s == service.FilterArchived {
		f.State = s
	}
	list, err := h.svc.ListScores(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	v := views.PlayerView{Player: pl, Sync: sum.Sync(), Counts: sum.Counts, Scores: list, Filter: f, Now: h.svc.Now()}
	if isHTMX(r) && r.Header.Get("HX-Target") == "scores" {
		render(w, r, http.StatusOK, views.ScoreTable(v))
		return
	}
	p := h.page(r, pl.Name)
	p.OG = &views.OpenGraph{
		Title:       pl.Name + " · ScoreSaber replays",
		Description: fmt.Sprintf("%s archived replays", views.Number(sum.Counts.Archived)),
		Image:       pl.AvatarURL,
		URL:         httpx.BaseURLFrom(ctx) + "/p/" + pl.ID,
	}
	render(w, r, http.StatusOK, views.PlayerPage(p, v))
}

func (h *Handler) score(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		h.notFound(w, r, "No such score.")
		return
	}
	sc, err := h.svc.GetScore(ctx, id)
	if isNotFound(err) {
		h.notFound(w, r, "No such score.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	base := httpx.BaseURLFrom(ctx)
	v := views.ScoreView{
		Score: sc, ViewerAvailable: h.viewer.Available(), Now: h.svc.Now(),
		ReplayURL: base + views.ReplayPath(id), EmbedURL: base + views.EmbedPath(id),
	}
	v.EmbedCode = views.EmbedSnippet(v.EmbedURL)
	title := fmt.Sprintf("%s by %s", views.SongTitle(sc), views.PlayerName(sc))
	p := h.page(r, title)
	p.OG = &views.OpenGraph{Title: title, Description: views.ScoreSummary(sc), Image: views.CoverURL(sc), URL: base + views.ScoreURL(id)}
	render(w, r, http.StatusOK, views.ScorePage(p, v))
}
