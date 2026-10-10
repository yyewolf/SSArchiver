package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type FeedPath struct {
	IdentityPath
	Kind string `path:"kind" enum:"score,attempt"`
}

type UpdateFeedInput struct {
	FeedPath
	Body struct {
		Enabled bool `json:"enabled" doc:"Archive this feed"`
	}
}

type FeedOutput struct{ Body Feed }

func (a *API) registerFeeds() {
	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "update-feed", Method: http.MethodPatch, Path: "/api/v1/players/{id}/identities/{platform}/feeds/{kind}",
		Summary: "Switch an optional feed on or off", Tags: []string{"Players"},
		Description: "Only optional feeds (BeatLeader attempts). Switching one on checks access at once: a private feed " +
			"comes back with `access: private` and a `hint` saying what the player must change. 422 for required feeds.",
	}), func(ctx context.Context, in *UpdateFeedInput) (*FeedOutput, error) {
		k, err := a.feedKey(ctx, in.FeedPath)
		if err != nil {
			return nil, err
		}
		f, err := a.svc.SetFeedEnabled(ctx, k, in.Body.Enabled)
		if err != nil {
			return nil, mapErr(err)
		}
		if in.Body.Enabled {
			// A failed probe is recorded on the feed (last_error); the response shows it.
			// A rate-limited one records nothing, so it answers 429 instead.
			checked, perr := a.svc.CheckFeedAccess(ctx, k)
			if errors.Is(perr, platform.ErrRateLimited) {
				return nil, mapErr(perr)
			}
			if checked != nil {
				f = checked
			} else if perr != nil {
				return nil, mapErr(perr)
			}
		}
		return a.feedOutput(ctx, k, f)
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "check-feed", Method: http.MethodPost, Path: "/api/v1/players/{id}/identities/{platform}/feeds/{kind}/check",
		Summary: "Check a feed's access again", Tags: []string{"Players"},
		Description: "Probes the platform now (the worker also re-checks private feeds every 24 hours).",
	}), func(ctx context.Context, in *FeedPath) (*FeedOutput, error) {
		k, err := a.feedKey(ctx, *in)
		if err != nil {
			return nil, err
		}
		f, err := a.svc.CheckFeedAccess(ctx, k)
		if f == nil || errors.Is(err, platform.ErrRateLimited) { // a throttled probe verified nothing
			return nil, mapErr(err)
		}
		return a.feedOutput(ctx, k, f)
	})
}

func (a *API) feedKey(ctx context.Context, in FeedPath) (service.FeedKey, error) {
	id, err := a.playerID(ctx, in.ID)
	if err != nil {
		return service.FeedKey{}, err
	}
	if _, err := a.platformName(in.Platform); err != nil {
		return service.FeedKey{}, err
	}
	return service.FeedKey{PlayerID: id, Platform: in.Platform, Kind: in.Kind}, nil
}

// feedOutput answers with the feed as the player DTO shows it (with counts);
// a feed switched off before it ever existed has no row, so f stands in.
func (a *API) feedOutput(ctx context.Context, k service.FeedKey, f *model.SyncFeed) (*FeedOutput, error) {
	sum, err := a.svc.GetPlayerSummary(ctx, k.PlayerID)
	if err != nil {
		return nil, mapErr(err)
	}
	p, _ := a.svc.Platforms().Get(k.Platform)
	for _, id := range sum.Identities {
		if id.Platform != k.Platform {
			continue
		}
		for _, fd := range id.Feeds {
			if fd.Feed == k.Kind {
				return &FeedOutput{Body: feedDTO(p, fd, id.Counts[fd.Feed])}, nil
			}
		}
	}
	if f == nil {
		return nil, mapErr(errors.Join(service.ErrNotFound, errors.New("feed "+k.String())))
	}
	return &FeedOutput{Body: feedDTO(p, *f, service.Counts{})}, nil
}
