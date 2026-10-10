// Package api exposes SSArchiver over a JSON API built with huma.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/platform"
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
	cfg.Info.Description = "Archived Beat Saber replays (ScoreSaber, BeatLeader). Read endpoints are public; write endpoints need the admin session cookie."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"session": {Type: "apiKey", In: "cookie", Name: httpx.SessionCookie},
	}
	a := &API{svc: svc, status: status}
	inner := http.NewServeMux()
	a.api = humago.New(inner, cfg)
	a.registerPlayers()
	a.registerIdentities()
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

// Problem is an RFC 9457 problem with a machine-readable code (spec §6.2).
type Problem struct {
	huma.ErrorModel
	Code     string `json:"code,omitempty" doc:"Machine-readable reason"`
	PlayerID string `json:"player_id,omitempty" doc:"The other player (identity_linked_elsewhere)"`
}

func problem(status int, code, msg string) *Problem {
	return &Problem{ErrorModel: huma.ErrorModel{Status: status, Title: http.StatusText(status), Detail: msg}, Code: code}
}

func mapErr(err error) error {
	var le *service.LinkedElsewhereError
	switch {
	case errors.As(err, &le):
		p := problem(http.StatusConflict, "identity_linked_elsewhere", err.Error())
		p.PlayerID = le.PlayerID
		return p
	case errors.Is(err, service.ErrPlatformAlreadyLinked):
		return problem(http.StatusConflict, "platform_already_linked", err.Error())
	case errors.Is(err, service.ErrLastIdentity):
		return problem(http.StatusConflict, "last_identity", err.Error())
	case errors.Is(err, service.ErrMergeConflict):
		return problem(http.StatusConflict, "merge_platform_conflict", err.Error())
	case errors.Is(err, service.ErrMergeSelf), errors.Is(err, service.ErrInvalidPlayerRef):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, service.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, service.ErrPlayerExists):
		return huma.Error409Conflict(err.Error())
	}
	slog.Error("api request failed", "err", err)
	return huma.Error500InternalServerError("internal error")
}

// playerID resolves a path ID, following merge aliases (spec §6.2).
func (a *API) playerID(ctx context.Context, id string) (string, error) {
	cur, _, err := a.svc.ResolvePlayerID(ctx, id)
	if err != nil {
		return "", mapErr(err)
	}
	return cur, nil
}

// platformName checks a platform name against the registry.
func (a *API) platformName(name string) (platform.Platform, error) {
	p, ok := a.svc.Platforms().Get(name)
	if !ok {
		return platform.Platform{}, huma.Error422UnprocessableEntity("unknown platform " + strconv.Quote(name) + "; registered: " + a.platformList())
	}
	return p, nil
}

// platformList names the registered platforms, for errors and descriptions.
func (a *API) platformList() string {
	var names []string
	for _, p := range a.svc.Platforms().All() {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}
