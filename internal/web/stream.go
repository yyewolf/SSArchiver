package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

// streamHeartbeat keeps idle streams alive through proxies and detects dead peers.
const streamHeartbeat = 20 * time.Second

type sseEvent struct{ name, data string }

// stream pushes live updates to public pages. Without ?player it serves the
// home page (one card event per player); with it, the player page's three
// regions: stats, accounts and progress.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if id := r.URL.Query().Get("player"); id != "" {
		playerID, _, err := h.svc.ResolvePlayerID(ctx, id)
		if err != nil {
			if isNotFound(err) {
				h.notFound(w, r, "This player is not archived here.")
			} else {
				h.serverError(w, r, err)
			}
			return
		}
		sub := h.svc.Subscribe(func(u service.Update) bool { return u.PlayerID == playerID })
		defer sub.Close()
		events := h.playerEvents(ctx, playerID)
		h.serveStream(w, r, sub, events, func(service.Update) []sseEvent { return events() })
		return
	}
	sub := h.svc.Subscribe(func(u service.Update) bool { return u.PlayerID != "" })
	defer sub.Close()
	h.serveStream(w, r, sub, h.homeSnapshot(ctx), h.homeEvent(ctx))
}

// syncStream pushes live updates to the admin sync page: the live section and
// activity table on every change, the failed replays table on replay changes.
func (h *Handler) syncStream(w http.ResponseWriter, r *http.Request) {
	sub := h.svc.Subscribe(nil)
	defer sub.Close()
	snapshot := func() []sseEvent { return h.syncRegions(r.Context(), service.Update{}) }
	h.serveStream(w, r, sub, snapshot, func(u service.Update) []sseEvent {
		return h.syncRegions(r.Context(), u)
	})
}

func (h *Handler) serveStream(w http.ResponseWriter, r *http.Request, sub *service.Subscription, snapshot func() []sseEvent, update func(service.Update) []sseEvent) {
	// ResponseController unwraps middleware writers (httpx's statusRecorder)
	// to reach the flushing support underneath.
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		slog.Error("stream: flushing unsupported", "path", r.URL.Path, "err", err)
		return
	}

	write := func(events []sseEvent) bool {
		if len(events) == 0 {
			return true
		}
		if err := writeSSE(w, events); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write(snapshot()) {
		return
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case u := <-sub.C:
			if !write(update(u)) {
				return
			}
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		}
	}
}

// writeSSE frames events; HTML data is split into one data: line per source line.
func writeSSE(w io.Writer, events []sseEvent) error {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "event: %s\n", e.name)
		for _, line := range strings.Split(e.data, "\n") {
			fmt.Fprintf(&b, "data: %s\n", line)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// playerEvents renders the player page's live regions; an empty list is sent
// when the player is gone (the stream then idles until the client reloads).
func (h *Handler) playerEvents(ctx context.Context, id string) func() []sseEvent {
	return func() []sseEvent {
		sum, err := h.svc.GetPlayerSummary(ctx, id)
		if err != nil {
			slog.Error("stream: player summary failed", "player", id, "err", err)
			return nil
		}
		v := views.PlayerView{Player: &sum.Player, Summary: sum, Platforms: h.accountPlatforms(sum), Now: h.svc.Now()}
		var out []sseEvent
		for _, region := range []struct {
			name string
			c    templ.Component
		}{
			{"stats", views.PlayerStats(v)},
			{"accounts", views.PlayerAccounts(v)},
			{"progress", views.ArchiveProgressContent(v)},
		} {
			data, err := renderComponent(ctx, region.c)
			if err != nil {
				slog.Error("stream: render failed", "region", region.name, "err", err)
				continue
			}
			out = append(out, sseEvent{region.name, data})
		}
		return out
	}
}

// homeSnapshot refreshes every card on connect so reconnects self-heal.
func (h *Handler) homeSnapshot(ctx context.Context) func() []sseEvent {
	return func() []sseEvent {
		players, err := h.svc.ListPlayers(ctx, false)
		if err != nil {
			slog.Error("stream: list players failed", "err", err)
			return nil
		}
		var out []sseEvent
		for _, pl := range players {
			data, err := renderComponent(ctx, views.PlayerCard(pl))
			if err != nil {
				slog.Error("stream: render card failed", "player", pl.ID, "err", err)
				continue
			}
			out = append(out, sseEvent{"card/" + pl.ID, data})
		}
		return out
	}
}

// homeEvent refreshes one card; a deleted player's card is removed.
func (h *Handler) homeEvent(ctx context.Context) func(service.Update) []sseEvent {
	return func(u service.Update) []sseEvent {
		sum, err := h.svc.GetPlayerSummary(ctx, u.PlayerID)
		if isNotFound(err) {
			return []sseEvent{{"card/" + u.PlayerID, ""}}
		}
		if err != nil {
			slog.Error("stream: player summary failed", "player", u.PlayerID, "err", err)
			return nil
		}
		data, err := renderComponent(ctx, views.PlayerCard(sum))
		if err != nil {
			slog.Error("stream: render card failed", "player", u.PlayerID, "err", err)
			return nil
		}
		return []sseEvent{{"card/" + u.PlayerID, data}}
	}
}

// syncRegions renders the admin sync page's live regions. The activity table
// is always the unfiltered view: a client with active filters does not listen.
func (h *Handler) syncRegions(ctx context.Context, u service.Update) []sseEvent {
	var out []sseEvent
	add := func(name string, c templ.Component) {
		data, err := renderComponent(ctx, c)
		if err != nil {
			slog.Error("stream: render failed", "region", name, "err", err)
			return
		}
		out = append(out, sseEvent{name, data})
	}
	if v, err := h.syncView(ctx); err != nil {
		slog.Error("stream: sync view failed", "err", err)
	} else {
		add("sync", views.SyncLive(v))
	}
	if ev, err := h.eventsViewFilter(ctx, service.EventFilter{Page: 1, PerPage: 50}); err != nil {
		slog.Error("stream: events view failed", "err", err)
	} else {
		add("events", views.EventsTable(ev))
	}
	if u.Kind == "" || u.Kind == model.KindReplay {
		if fv, err := h.failedViewPage(ctx, 1); err != nil {
			slog.Error("stream: failed view failed", "err", err)
		} else {
			add("failed", views.FailedTable(fv))
		}
	}
	return out
}

func renderComponent(ctx context.Context, c templ.Component) (string, error) {
	var buf bytes.Buffer
	if err := c.Render(ctx, &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}
