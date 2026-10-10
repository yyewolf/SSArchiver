package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
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
	id, aliased, err := h.svc.ResolvePlayerID(ctx, r.PathValue("id"))
	if isNotFound(err) {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if aliased { // merged away: the survivor's page (spec §6.1)
		target := "/p/" + url.PathEscape(id)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		// #nosec G710 -- same-site /p/ path; only the query string passes through
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	sum, err := h.svc.GetPlayerSummary(ctx, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	f := scoreFilter(r.URL.Query(), id, h.svc.Platforms())
	groups, err := h.svc.ListMapGroups(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plats := h.accountPlatforms(sum)
	v := views.PlayerView{Player: &sum.Player, Summary: sum, Groups: groups, Filter: f, Platforms: plats, Now: h.svc.Now()}
	v.HasAttempts = hasAttempts(sum)
	if isHTMX(r) && r.Header.Get("HX-Target") == "scores" {
		render(w, r, http.StatusOK, views.ScoreTable(v))
		return
	}
	names := make([]string, 0, len(plats))
	for _, p := range plats {
		names = append(names, p.DisplayName)
	}
	p := h.page(r, sum.Name)
	p.OG = &views.OpenGraph{
		Title:       sum.Name + " · " + strings.Join(names, " & ") + " replays",
		Description: fmt.Sprintf("%s archived replays", views.Number(sum.Counts.Archived)),
		Image:       sum.AvatarURL,
		URL:         httpx.BaseURLFrom(ctx) + "/p/" + id,
	}
	render(w, r, http.StatusOK, views.PlayerPage(p, v))
}

// playerMap is the htmx fragment with every play of one map (spec §6.1); it
// follows merge aliases so old links keep serving the survivor's plays.
func (h *Handler) playerMap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _, err := h.svc.ResolvePlayerID(ctx, r.PathValue("id"))
	if isNotFound(err) {
		h.notFound(w, r, "No such map.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	if q.Get("key") == "" {
		h.notFound(w, r, "No such map.")
		return
	}
	f := scoreFilter(q, id, h.svc.Platforms())
	f.MapKey, f.Page, f.PerPage = q.Get("key"), 1, 100
	list, err := h.svc.ListScores(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.MapPlays(views.MapPlaysView{Plays: list, Now: h.svc.Now()}))
}

// scoreFilter reads the player page's filters; unknown values are ignored.
func scoreFilter(q url.Values, playerID string, reg *platform.Registry) service.ScoreFilter {
	page, _ := strconv.Atoi(q.Get("page"))
	f := service.ScoreFilter{PlayerID: playerID, Search: q.Get("q"), RankedOnly: q.Get("ranked") == "1", Page: page, PerPage: 50}
	if s := q.Get("state"); s == service.FilterWithReplay || s == service.FilterArchived {
		f.State = s
	}
	if p := q.Get("platform"); p != "" {
		if _, ok := reg.Get(p); ok {
			f.Platform = p
		}
	}
	f.MinScore, f.MaxScore = scoreBound(q.Get("min_score")), scoreBound(q.Get("max_score"))
	if types, err := service.ParseTypes(q.Get("type")); err == nil {
		f.Types = types
	}
	return f
}

func scoreBound(s string) *int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

// hasAttempts reports whether a player has any stored attempt.
func hasAttempts(sum service.PlayerSummary) bool {
	for _, id := range sum.Identities {
		if id.Counts[model.KindAttempt].Scores > 0 {
			return true
		}
	}
	return false
}

// accountPlatforms are the registry entries of the player's accounts, primary first.
func (h *Handler) accountPlatforms(sum service.PlayerSummary) []platform.Platform {
	var out []platform.Platform
	for _, id := range sum.Identities {
		if p, ok := h.svc.Platforms().Get(id.Platform); ok {
			out = append(out, p)
		}
	}
	return out
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
	arcOK, blOK := h.viewer.Available(), views.BeatLeaderPlays(ctx, sc)
	// ?viewer= remembers the choice in the visitor's cookie; otherwise the
	// cookie, then the row's platform default, applies.
	viewer := ""
	if choice := r.URL.Query().Get("viewer"); choice == service.ViewerBeatLeader || choice == service.ViewerArcViewer {
		h.setViewerCookie(w, r, choice)
		viewer = pickViewer(choice, arcOK, blOK)
	}
	if viewer == "" {
		if c, cerr := r.Cookie(viewerCookie); cerr == nil {
			viewer = pickViewer(c.Value, arcOK, blOK)
		}
	}
	if viewer == "" {
		viewer = pickViewer(platformViewer(sc.Platform), arcOK, blOK)
	}
	embedPath := views.EmbedPath(ctx, sc)
	if viewer != "" {
		embedPath += "?viewer=" + viewer
	}
	v := views.ScoreView{
		Score: sc, Viewer: viewer, ArcAvailable: arcOK, Now: h.svc.Now(),
		ReplayURL: base + views.ReplayPath(ctx, sc), EmbedURL: base + embedPath, EmbedPathURL: embedPath,
	}
	v.EmbedCode = views.EmbedSnippet(v.EmbedURL)
	title := fmt.Sprintf("%s by %s", views.SongTitle(sc), views.PlayerName(sc))
	if sc.Kind == model.KindAttempt {
		title = fmt.Sprintf("%s · %s attempt by %s", views.SongTitle(sc), views.EndLabel(sc.EndType), views.PlayerName(sc))
	}
	p := h.page(r, title)
	p.OG = &views.OpenGraph{Title: title, Description: views.ScoreSummary(sc), Image: views.CoverURL(sc), URL: base + views.ScoreURL(ctx, sc)}
	render(w, r, http.StatusOK, views.ScorePage(p, v))
}
