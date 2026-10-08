// Package api exposes SSArchiver over a JSON API built with huma.
package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type StatusSource interface {
	Status() archiver.Status
}

type API struct {
	svc    *service.Service
	status StatusSource
	api    huma.API
}

var adminSecurity = []map[string][]string{{"session": {}}}

// Register mounts the API and its docs on mux.
func Register(mux *http.ServeMux, svc *service.Service, status StatusSource, version string) huma.API {
	cfg := huma.DefaultConfig("SSArchiver API", version)
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "/api/docs"
	cfg.SchemasPath = "/api/schemas"
	cfg.Info.Description = "Archived ScoreSaber replays. Read endpoints are public; write endpoints need the admin session cookie."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"session": {Type: "apiKey", In: "cookie", Name: httpx.SessionCookie},
	}
	a := &API{svc: svc, status: status}
	inner := http.NewServeMux()
	a.api = humago.New(inner, cfg)
	a.registerPlayers()
	a.registerScores()
	a.registerSync()
	mux.Handle("/api/", httpx.CORSReads("/api/v1/sync")(inner))
	return a.api
}

func (a *API) requireAdmin(ctx huma.Context, next func(huma.Context)) {
	if httpx.UserFrom(ctx.Context()) == nil {
		_ = huma.WriteErr(a.api, ctx, http.StatusUnauthorized, "admin session required")
		return
	}
	next(ctx)
}

func (a *API) admin(op huma.Operation) huma.Operation {
	op.Security = adminSecurity
	op.Middlewares = huma.Middlewares{a.requireAdmin}
	op.Tags = append(op.Tags, "Admin")
	return op
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, service.ErrPlayerExists):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, service.ErrInvalidPlayerRef):
		return huma.Error422UnprocessableEntity(err.Error())
	}
	slog.Error("api request failed", "err", err)
	return huma.Error500InternalServerError("internal error")
}
