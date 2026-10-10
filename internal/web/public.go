package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
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
	displayName := "ScoreSaber"
	profileURL := ""
	if len(sum.Identities) > 0 {
		if p, ok := h.svc.Platforms().Get(sum.Identities[0].Platform); ok {
			displayName = p.DisplayName
			profileURL = p.ProfileURL(sum.Identities[0].ExternalID)
		}
	}
	v := views.PlayerView{Player: pl, Sync: sum.Sync(), Counts: sum.Counts, Scores: list, Filter: f, Now: h.svc.Now(), ProfileURL: profileURL, Platform: displayName}
	if isHTMX(r) && r.Header.Get("HX-Target") == "scores" {
		render(w, r, http.StatusOK, views.ScoreTable(v))
		return
	}
	p := h.page(r, pl.Name)
	p.OG = &views.OpenGraph{
		Title:       pl.Name + " · " + displayName + " replays",
		Description: fmt.Sprintf("%s archived replays", views.Number(sum.Counts.Archived)),
		Image:       pl.AvatarURL,
		URL:         httpx.BaseURLFrom(ctx) + "/p/" + pl.ID,
	}
	render(w, r, http.StatusOK, views.PlayerPage(p, v))
}

// score serves the legacy /s/{id} (ScoreSaber scores).
func (h *Handler) score(w http.ResponseWriter, r *http.Request) {
	sc, err := h.legacyPlay(r, r.PathValue("id"))
	h.scorePage(w, r, sc, err)
}

// platformScore serves /s/{slug}/{externalID} and /s/{slug}/attempt/{externalID}.
func (h *Handler) platformScore(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, err := h.slugPlay(r, kind, r.PathValue("externalID"))
		h.scorePage(w, r, sc, err)
	}
}

func (h *Handler) scorePage(w http.ResponseWriter, r *http.Request, sc *model.Score, err error) {
	if isNotFound(err) {
		h.notFound(w, r, "No such score.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	ctx := r.Context()
	base := httpx.BaseURLFrom(ctx)
	v := views.ScoreView{
		Score: sc, ViewerAvailable: h.viewer.Available(), Now: h.svc.Now(),
		ReplayURL: base + views.ReplayPath(ctx, sc), EmbedURL: base + views.EmbedPath(ctx, sc),
	}
	v.EmbedCode = views.EmbedSnippet(v.EmbedURL)
	title := fmt.Sprintf("%s by %s", views.SongTitle(sc), views.PlayerName(sc))
	p := h.page(r, title)
	p.OG = &views.OpenGraph{Title: title, Description: views.ScoreSummary(sc), Image: views.CoverURL(sc), URL: base + views.ScoreURL(ctx, sc)}
	render(w, r, http.StatusOK, views.ScorePage(p, v))
}
