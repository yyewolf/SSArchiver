package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

func (h *Handler) syncView(ctx context.Context) (views.SyncView, error) {
	st, err := h.svc.Settings(ctx)
	if err != nil {
		return views.SyncView{}, err
	}
	players, err := h.svc.ListPlayers(ctx, true)
	if err != nil {
		return views.SyncView{}, err
	}
	now := h.svc.Now()
	var enabled, withPending int
	var pending, failed int64
	for _, p := range players {
		if p.Enabled {
			enabled++
		}
		for _, id := range p.Identities {
			for _, c := range id.Counts {
				pending += c.Pending
				failed += c.Failed
			}
			for _, f := range id.Feeds {
				if p.Enabled && id.Enabled && f.Enabled && id.Counts[f.Feed].Pending > 0 {
					withPending++
				}
			}
		}
	}
	rate := service.ReplayRatePerHour(h.cfg.HourlyBudget, enabled, st.PollInterval)
	var rows []views.QueueRow
	for _, p := range players {
		for _, id := range p.Identities {
			for _, f := range id.Feeds {
				q := views.QueueRow{Player: p, Identity: id, Feed: f, Counts: id.Counts[f.Feed], NextPoll: now}
				if f.LastPolledAt != nil {
					q.NextPoll = f.LastPolledAt.Add(st.PollInterval)
				}
				if p.Enabled && id.Enabled && f.Enabled && withPending > 0 {
					q.ETA = service.ETA(q.Counts.Pending, rate/float64(withPending))
				}
				rows = append(rows, q)
			}
		}
	}
	return views.SyncView{
		Status: h.status.Status(), Paused: st.WorkerPaused, Queues: rows,
		PendingTotal: pending, FailedTotal: failed, RatePerHour: rate, ETA: service.ETA(pending, rate), Now: now,
	}, nil
}

func (h *Handler) eventsView(r *http.Request) (views.EventsView, error) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	f := service.EventFilter{
		Level: q.Get("level"), Kind: q.Get("kind"), PlayerID: q.Get("player"),
		Platform: q.Get("platform"), Feed: q.Get("feed"), Page: max(page, 1), PerPage: 50,
	}
	events, total, err := h.svc.ListEvents(r.Context(), f)
	if err != nil {
		return views.EventsView{}, err
	}
	players, err := h.svc.ListPlayers(r.Context(), true)
	return views.EventsView{Events: events, Total: total, Filter: f, Players: players, Platforms: h.svc.Platforms().All(), Now: h.svc.Now()}, err
}

func (h *Handler) failedView(r *http.Request) (views.FailedView, error) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	items, total, err := h.svc.ListFailedReplays(r.Context(), page, 50)
	pages := int((total + 49) / 50)
	return views.FailedView{Items: items, Total: total, Page: page, Pages: max(pages, 1), Now: h.svc.Now()}, err
}

func (h *Handler) syncPage(w http.ResponseWriter, r *http.Request) {
	v, err := h.syncView(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	ev, err := h.eventsView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	fv, err := h.failedView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.SyncPage(h.page(r, "Sync"), v, ev, fv))
}

func (h *Handler) syncLive(w http.ResponseWriter, r *http.Request) {
	v, err := h.syncView(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.SyncLive(v))
}

func (h *Handler) syncEvents(w http.ResponseWriter, r *http.Request) {
	ev, err := h.eventsView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.EventsTable(ev))
}

func (h *Handler) syncFailed(w http.ResponseWriter, r *http.Request) {
	fv, err := h.failedView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.FailedTable(fv))
}

func (h *Handler) setPaused(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h.svc.SetWorkerPaused(r.Context(), paused); err != nil {
			h.serverError(w, r, err)
			return
		}
		v, err := h.syncView(r.Context())
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		title := "Worker resumed"
		if paused {
			title = "Worker paused"
		}
		render(w, r, http.StatusOK, views.SyncLiveToast(v, title))
	}
}

func (h *Handler) syncRetry(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	scoreID, _ := strconv.ParseInt(r.PostFormValue("score_id"), 10, 64)
	n, err := h.svc.RetryFailed(r.Context(), r.PostFormValue("player_id"), scoreID)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	fv, err := h.failedView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.FailedRetried(fv, n))
}
