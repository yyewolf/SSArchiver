package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

type IdentityPath struct {
	PlayerPath
	Platform string `path:"platform" maxLength:"32" example:"beatleader"`
}

type LinkIdentityInput struct {
	PlayerPath
	Body struct {
		Platform string `json:"platform" minLength:"1" maxLength:"32" example:"beatleader"`
		Ref      string `json:"ref" minLength:"1" maxLength:"200" doc:"Profile URL or player ID on that platform"`
	}
}

type UpdateIdentityInput struct {
	IdentityPath
	Body struct {
		Enabled *bool `json:"enabled,omitempty" doc:"Pause or resume this account"`
	}
}

type UnlinkIdentityInput struct {
	IdentityPath
	DeleteFiles bool `query:"delete_files" doc:"Also delete this account's archived replay files"`
}

type MergeInput struct {
	Source string `path:"source" pattern:"^[a-z0-9-]{1,40}$" doc:"The player merged away"`
	Body   struct {
		Into string `json:"into" pattern:"^[a-z0-9-]{1,40}$" doc:"The player that receives every account, score and replay"`
	}
}

func (a *API) registerIdentities() {
	plats := "Platforms: " + a.platformList() + "."

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "link-identity", Method: http.MethodPost, Path: "/api/v1/players/{id}/identities",
		Summary: "Link a platform account to a player", Tags: []string{"Players"},
		Description: plats + " 409 `identity_linked_elsewhere` (with `player_id`) when the account is tracked as another player — merge them instead; 409 `platform_already_linked` when this player already has an account there.",
	}), func(ctx context.Context, in *LinkIdentityInput) (*PlayerOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if _, err := a.platformName(in.Body.Platform); err != nil {
			return nil, err
		}
		if _, err := a.svc.LinkIdentity(ctx, id, in.Body.Ref, in.Body.Platform); err != nil {
			return nil, mapErr(err)
		}
		return a.playerOutput(ctx, id)
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "update-identity", Method: http.MethodPatch, Path: "/api/v1/players/{id}/identities/{platform}",
		Summary: "Pause or resume one platform account", Tags: []string{"Players"}, Description: plats,
	}), func(ctx context.Context, in *UpdateIdentityInput) (*PlayerOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if _, err := a.platformName(in.Platform); err != nil {
			return nil, err
		}
		if in.Body.Enabled != nil {
			if err := a.svc.SetIdentityEnabled(ctx, id, in.Platform, *in.Body.Enabled); err != nil {
				return nil, mapErr(err)
			}
		}
		return a.playerOutput(ctx, id)
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "unlink-identity", Method: http.MethodDelete, Path: "/api/v1/players/{id}/identities/{platform}",
		DefaultStatus: http.StatusNoContent, Summary: "Unlink a platform account and remove its scores", Tags: []string{"Players"},
		Description: plats + " 409 `last_identity`: a player keeps at least one account (delete the player instead).",
	}), func(ctx context.Context, in *UnlinkIdentityInput) (*struct{}, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if _, err := a.platformName(in.Platform); err != nil {
			return nil, err
		}
		if err := a.svc.UnlinkIdentity(ctx, id, in.Platform, in.DeleteFiles); err != nil {
			return nil, mapErr(err)
		}
		return nil, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "merge-player", Method: http.MethodPost, Path: "/api/v1/players/{source}/merge",
		Summary: "Merge a player into another one", Tags: []string{"Players"},
		Description: "Moves every account, score and replay of the source into `into`; the source ID keeps resolving to it. " +
			"409 `merge_platform_conflict` when both players have an account on the same platform.",
	}), func(ctx context.Context, in *MergeInput) (*PlayerOutput, error) {
		src, err := a.playerID(ctx, in.Source)
		if err != nil {
			return nil, err
		}
		into, err := a.playerID(ctx, in.Body.Into)
		if err != nil {
			return nil, err
		}
		if err := a.svc.MergePlayers(ctx, src, into); err != nil {
			return nil, mapErr(err)
		}
		return a.playerOutput(ctx, into)
	})
}

func (a *API) playerOutput(ctx context.Context, id string) (*PlayerOutput, error) {
	dto, err := a.summary(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &PlayerOutput{Body: dto}, nil
}
