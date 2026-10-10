package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type ListScoresInput struct {
	PlayerPath
	Page     int    `query:"page" minimum:"1" default:"1"`
	PerPage  int    `query:"per_page" minimum:"1" maximum:"100" default:"50"`
	Search   string `query:"search" maxLength:"64" doc:"Matches song name, artist or mapper"`
	State    string `query:"state" enum:"replay,archived" doc:"replay: the platform offered a replay; archived: stored here"`
	Ranked   bool   `query:"ranked" doc:"Only ranked maps"`
	Platform string `query:"platform" maxLength:"32" doc:"all (default) or one platform name"`
	MinScore string `query:"min_score" pattern:"^[0-9]{1,12}$" doc:"Inclusive lower bound on the score"`
	MaxScore string `query:"max_score" pattern:"^[0-9]{1,12}$" doc:"Inclusive upper bound on the score"`
}

type ScorePage struct {
	Items   []Score `json:"items"`
	Total   int64   `json:"total"`
	Page    int     `json:"page"`
	PerPage int     `json:"per_page"`
	Pages   int     `json:"pages"`
}

type ListScoresOutput struct{ Body ScorePage }

type ScorePath struct {
	ID int64 `path:"id" minimum:"1"`
}

type PlayPath struct {
	Platform   string `path:"platform" maxLength:"32" example:"beatleader"`
	ExternalID string `path:"externalID" maxLength:"64" example:"12164051"`
}

type ScoreOutput struct{ Body Score }

// bound parses an optional digit-string bound (validated by the pattern).
func bound(s string) *int64 {
	if s == "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func (a *API) registerScores() {
	huma.Register(a.api, huma.Operation{
		OperationID: "list-player-scores", Method: http.MethodGet, Path: "/api/v1/players/{id}/scores",
		Summary: "List a player's scores, newest first", Tags: []string{"Scores"},
		Description: "Platforms: " + a.platformList() + ".",
	}, func(ctx context.Context, in *ListScoresInput) (*ListScoresOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		f := service.ScoreFilter{
			PlayerID: id, Search: in.Search, RankedOnly: in.Ranked, State: in.State, Page: in.Page, PerPage: in.PerPage,
			MinScore: bound(in.MinScore), MaxScore: bound(in.MaxScore),
		}
		if in.Platform != "" && in.Platform != "all" {
			if _, err := a.platformName(in.Platform); err != nil {
				return nil, err
			}
			f.Platform = in.Platform
		}
		list, err := a.svc.ListScores(ctx, f)
		if err != nil {
			return nil, mapErr(err)
		}
		base, reg := httpx.BaseURLFrom(ctx), a.svc.Platforms()
		out := &ListScoresOutput{Body: ScorePage{Items: make([]Score, 0, len(list.Items)), Total: list.Total, Page: list.Page, PerPage: list.PerPage, Pages: list.Pages}}
		for _, s := range list.Items {
			out.Body.Items = append(out.Body.Items, scoreDTO(base, reg, s))
		}
		return out, nil
	})

	huma.Register(a.api, huma.Operation{
		OperationID: "get-score", Method: http.MethodGet, Path: "/api/v1/scores/{id}",
		Summary: "Get a ScoreSaber score and its replay status", Tags: []string{"Scores"},
	}, func(ctx context.Context, in *ScorePath) (*ScoreOutput, error) {
		lp, ok := a.svc.Platforms().Legacy()
		if !ok {
			return nil, huma.Error404NotFound("no such score")
		}
		return a.play(ctx, lp.Name, model.KindScore, strconv.FormatInt(in.ID, 10))
	})

	for _, c := range []struct{ id, path, summary, kind string }{
		{"get-play", "/api/v1/scores/{platform}/{externalID}", "Get a score by its platform ID", model.KindScore},
		{"get-attempt", "/api/v1/scores/{platform}/attempt/{externalID}", "Get an attempt by its platform ID", model.KindAttempt},
	} {
		huma.Register(a.api, huma.Operation{
			OperationID: c.id, Method: http.MethodGet, Path: c.path, Summary: c.summary, Tags: []string{"Scores"},
			Description: "Platforms: " + a.platformList() + ".",
		}, func(ctx context.Context, in *PlayPath) (*ScoreOutput, error) {
			return a.play(ctx, in.Platform, c.kind, in.ExternalID)
		})
	}
}

func (a *API) play(ctx context.Context, platformName, kind, externalID string) (*ScoreOutput, error) {
	s, err := a.svc.GetPlay(ctx, platformName, kind, externalID)
	if err != nil {
		return nil, mapErr(err)
	}
	return &ScoreOutput{Body: scoreDTO(httpx.BaseURLFrom(ctx), a.svc.Platforms(), s)}, nil
}
