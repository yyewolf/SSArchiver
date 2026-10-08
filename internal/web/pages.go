package web

import (
	"net/http"

	"github.com/yyewolf/ssarchiver/internal/web/views"
)

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	players, err := h.svc.ListPlayers(r.Context(), false)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.Home(h.page(r, ""), players))
}
