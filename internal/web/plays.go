package web

import (
	"net/http"
	"strconv"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

// legacyPlay resolves the ID of a bare route (/s/{id}, /r/{id}.dat,
// /embed/{id}): scores of the legacy platform only, so no other platform's
// internal ID can shadow an old link (spec §6.1).
func (h *Handler) legacyPlay(r *http.Request, idStr string) (*model.Score, error) {
	id, ok := parseID(idStr)
	lp, legacy := h.svc.Platforms().Legacy()
	if !ok || !legacy {
		return nil, service.ErrNotFound
	}
	return h.svc.GetPlay(r.Context(), lp.Name, model.KindScore, strconv.FormatInt(id, 10))
}

// slugPlay resolves /…/{slug}/[attempt/]{externalID}; unknown slugs are not found.
func (h *Handler) slugPlay(r *http.Request, kind, externalID string) (*model.Score, error) {
	p, ok := h.svc.Platforms().BySlug(r.PathValue("slug"))
	if !ok {
		return nil, service.ErrNotFound
	}
	return h.svc.GetPlay(r.Context(), p.Name, kind, externalID)
}
