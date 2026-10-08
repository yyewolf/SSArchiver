package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type ListScoresInput struct {
	PlayerPath
	Page    int    `query:"page" minimum:"1" default:"1"`
	PerPage int    `query:"per_page" minimum:"1" maximum:"100" default:"50"`
	Search  string `query:"search" maxLength:"64" doc:"Matches song name, artist or mapper"`
	State   string `query:"state" enum:"replay,archived" doc:"replay: ScoreSaber offered a replay; archived: stored here"`
	Ranked  bool   `query:"ranked" doc:"Only ranked maps"`
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

type ScoreOutput struct{ Body Score }

func (a *API) registerScores() {
	huma.Register(a.api, huma.Operation{
		OperationID: "list-player-scores", Method: http.MethodGet, Path: "/api/v1/players/{id}/scores",
		Summary: "List a player's scores, newest first", Tags: []string{"Scores"},
	}, func(ctx context.Context, in *ListScoresInput) (*ListScoresOutput, error) {
		if _, err := a.svc.GetPlayer(ctx, in.ID); err != nil {
			return nil, mapErr(err)
		}
		list, err := a.svc.ListScores(ctx, service.ScoreFilter{
			PlayerID: in.ID, Search: in.Search, RankedOnly: in.Ranked, State: in.State, Page: in.Page, PerPage: in.PerPage,
		})
		if err != nil {
			return nil, mapErr(err)
		}
		base := httpx.BaseURLFrom(ctx)
		out := &ListScoresOutput{Body: ScorePage{Items: make([]Score, 0, len(list.Items)), Total: list.Total, Page: list.Page, PerPage: list.PerPage, Pages: list.Pages}}
		for _, s := range list.Items {
			out.Body.Items = append(out.Body.Items, scoreDTO(base, s))
		}
		return out, nil
	})

	huma.Register(a.api, huma.Operation{
		OperationID: "get-score", Method: http.MethodGet, Path: "/api/v1/scores/{id}",
		Summary: "Get a score and its replay status", Tags: []string{"Scores"},
	}, func(ctx context.Context, in *ScorePath) (*ScoreOutput, error) {
		s, err := a.svc.GetScore(ctx, in.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &ScoreOutput{Body: scoreDTO(httpx.BaseURLFrom(ctx), s)}, nil
	})
}
