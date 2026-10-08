package web

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

func (h *Handler) player(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	pl, err := h.svc.GetPlayer(ctx, id)
	if isNotFound(err) {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
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
	counts, err := h.svc.PlayerCounts(ctx, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	v := views.PlayerView{Player: pl, Counts: counts, Scores: list, Filter: f, Now: h.svc.Now()}
	if isHTMX(r) && r.Header.Get("HX-Target") == "scores" {
		render(w, r, http.StatusOK, views.ScoreTable(v))
		return
	}
	p := h.page(r, pl.Name)
	p.OG = &views.OpenGraph{
		Title:       pl.Name + " · ScoreSaber replays",
		Description: fmt.Sprintf("%s archived replays", views.Number(counts.Archived)),
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
