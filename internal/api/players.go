package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/yyewolf/ssarchiver/internal/httpx"
)

type PlayerPath struct {
	ID string `path:"id" pattern:"^[a-z0-9-]{1,40}$" doc:"Player ID (opaque; see identities[] for platform IDs)"`
}

type AccountPath struct {
	Platform   string `path:"platform" maxLength:"32" example:"scoresaber"`
	ExternalID string `path:"externalID" maxLength:"64" example:"76561198038925092"`
}

type PlayerOutput struct{ Body Player }

type ListPlayersOutput struct{ Body []Player }

type AddPlayerInput struct {
	Body struct {
		Ref      string `json:"ref" minLength:"1" maxLength:"200" doc:"Profile URL (the platform is detected) or player ID"`
		Platform string `json:"platform,omitempty" maxLength:"32" doc:"Platform of a bare ID (default: scoresaber). Profile URLs pick their platform."`
	}
}

type UpdatePlayerInput struct {
	PlayerPath
	Body struct {
		Enabled *bool `json:"enabled,omitempty" doc:"Pause or resume tracking"`
	}
}

type DeletePlayerInput struct {
	PlayerPath
	DeleteFiles bool `query:"delete_files" doc:"Also delete archived replay files from disk"`
}

func (a *API) summary(ctx context.Context, id string) (Player, error) {
	sum, err := a.svc.GetPlayerSummary(ctx, id)
	if err != nil {
		return Player{}, err
	}
	return playerDTO(httpx.BaseURLFrom(ctx), a.svc.Platforms(), sum), nil
}

func (a *API) registerPlayers() {
	huma.Register(a.api, huma.Operation{
		OperationID: "list-players", Method: http.MethodGet, Path: "/api/v1/players",
		Summary: "List tracked players", Tags: []string{"Players"},
	}, func(ctx context.Context, _ *struct{}) (*ListPlayersOutput, error) {
		ps, err := a.svc.ListPlayers(ctx, false)
		if err != nil {
			return nil, mapErr(err)
		}
		out := &ListPlayersOutput{Body: make([]Player, 0, len(ps))}
		for _, p := range ps {
			out.Body = append(out.Body, playerDTO(httpx.BaseURLFrom(ctx), a.svc.Platforms(), p))
		}
		return out, nil
	})

	huma.Register(a.api, huma.Operation{
		OperationID: "get-player", Method: http.MethodGet, Path: "/api/v1/players/{id}",
		Summary: "Get a player", Tags: []string{"Players"},
	}, func(ctx context.Context, in *PlayerPath) (*PlayerOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		p, err := a.summary(ctx, id)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: p}, nil
	})

	huma.Register(a.api, huma.Operation{
		OperationID: "get-player-by-account", Method: http.MethodGet, Path: "/api/v1/players/by/{platform}/{externalID}",
		Summary: "Find a player by a platform account", Tags: []string{"Players"},
	}, func(ctx context.Context, in *AccountPath) (*PlayerOutput, error) {
		pl, err := a.svc.PlayerByIdentity(ctx, in.Platform, in.ExternalID)
		if err != nil {
			return nil, mapErr(err)
		}
		dto, err := a.summary(ctx, pl.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: dto}, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "add-player", Method: http.MethodPost, Path: "/api/v1/players", DefaultStatus: http.StatusCreated,
		Summary: "Start tracking a player", Tags: []string{"Players"},
	}), func(ctx context.Context, in *AddPlayerInput) (*PlayerOutput, error) {
		p, err := a.svc.AddPlayer(ctx, in.Body.Ref, in.Body.Platform)
		if err != nil {
			return nil, mapErr(err)
		}
		dto, err := a.summary(ctx, p.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: dto}, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "update-player", Method: http.MethodPatch, Path: "/api/v1/players/{id}",
		Summary: "Update a tracked player", Tags: []string{"Players"},
	}), func(ctx context.Context, in *UpdatePlayerInput) (*PlayerOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if in.Body.Enabled != nil {
			if err := a.svc.SetPlayerEnabled(ctx, id, *in.Body.Enabled); err != nil {
				return nil, mapErr(err)
			}
		}
		dto, err := a.summary(ctx, id)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: dto}, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "delete-player", Method: http.MethodDelete, Path: "/api/v1/players/{id}", DefaultStatus: http.StatusNoContent,
		Summary: "Stop tracking a player and remove their scores", Tags: []string{"Players"},
	}), func(ctx context.Context, in *DeletePlayerInput) (*struct{}, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if err := a.svc.DeletePlayer(ctx, id, in.DeleteFiles); err != nil {
			return nil, mapErr(err)
		}
		return nil, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "poll-player", Method: http.MethodPost, Path: "/api/v1/players/{id}/poll", DefaultStatus: http.StatusAccepted,
		Summary: "Poll a player as soon as possible", Tags: []string{"Players"},
	}), func(ctx context.Context, in *PlayerPath) (*struct{}, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if err := a.svc.RequestPoll(ctx, id); err != nil {
			return nil, mapErr(err)
		}
		return nil, nil
	})
}
