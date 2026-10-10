package archiver

import (
	"context"
	"errors"
	"fmt"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

// feedEvent builds a sync event about one feed.
func feedEvent(wf *service.WorkFeed, level, kind, msg string) model.SyncEvent {
	return model.SyncEvent{
		Level: level, Kind: kind, PlayerID: service.Ptr(wf.PlayerID),
		Platform: service.Ptr(wf.Platform), Feed: service.Ptr(wf.Feed), Message: msg,
	}
}

func (w *Worker) poll(ctx context.Context, wf *service.WorkFeed) error {
	w.setStatus(StateRunning, "Polling "+wf.PlayerName)
	k := wf.Key()
	var newScores, newReplays, pagesRead, totalPages int
	reachedEnd := false
	for page := 1; page <= MaxPollPages; page++ {
		p, err := w.platform(wf.Platform)
		if err != nil {
			return err
		}
		pg, err := p.Adapter.FeedPage(ctx, wf.Feed, wf.ExternalID, page)
		if err != nil {
			return w.clientError(ctx, p, wf, err, true)
		}
		pagesRead, totalPages = page, pg.TotalPages
		if page == 1 && len(pg.Plays) > 0 && pg.Plays[0].Profile != nil {
			if err := w.svc.RefreshProfile(ctx, wf.PlayerID, wf.Platform, *pg.Plays[0].Profile); err != nil {
				return err
			}
		}
		res, err := w.svc.UpsertPlays(ctx, wf.PlayerID, wf.Platform, pg.Plays)
		if err != nil {
			return err
		}
		newScores += res.New
		newReplays += res.NewReplays
		if res.Known > 0 || len(pg.Plays) == 0 || page >= pg.TotalPages {
			reachedEnd = true
			break
		}
	}
	switch {
	case wf.BackfillState == model.BackfillPending && wf.BackfillPage <= 1:
		if reachedEnd {
			if err := w.svc.SetFeedBackfill(ctx, k, model.BackfillDone, pagesRead+1, totalPages); err != nil {
				return err
			}
		} else if err := w.svc.SetFeedBackfill(ctx, k, model.BackfillPending, MaxPollPages+1, totalPages); err != nil {
			return err
		}
	case !reachedEnd && wf.BackfillState == model.BackfillDone:
		if err := w.svc.SetFeedBackfill(ctx, k, model.BackfillPending, MaxPollPages+1, totalPages); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindBackfill, fmt.Sprintf(
			"more than %d pages of new scores since the last poll; resuming backfill from page %d", MaxPollPages, MaxPollPages+1)))
	}
	if err := w.svc.MarkFeedPolled(ctx, k); err != nil {
		return err
	}
	if newScores > 0 {
		w.svc.Log(ctx, feedEvent(wf, model.LevelInfo, model.KindScores, fmt.Sprintf("%d new %s, %d with replays", newScores, noun(wf.Feed), newReplays)))
	}
	return nil
}

func noun(kind string) string {
	if kind == model.KindScore {
		return "scores"
	}
	return kind + "s"
}

func (w *Worker) backfill(ctx context.Context, wf *service.WorkFeed) error {
	page := max(wf.BackfillPage, 1)
	w.setStatus(StateRunning, fmt.Sprintf("Backfilling %s · page %d", wf.PlayerName, page))
	p, err := w.platform(wf.Platform)
	if err != nil {
		return err
	}
	pg, err := p.Adapter.FeedPage(ctx, wf.Feed, wf.ExternalID, page)
	if err != nil {
		return w.clientError(ctx, p, wf, err, false)
	}
	if _, err := w.svc.UpsertPlays(ctx, wf.PlayerID, wf.Platform, pg.Plays); err != nil {
		return err
	}
	k := wf.Key()
	total := pg.TotalPages
	if len(pg.Plays) == 0 || page >= total {
		if err := w.svc.SetFeedBackfill(ctx, k, model.BackfillDone, page+1, total); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelInfo, model.KindBackfill, fmt.Sprintf("backfill listing complete (%d pages)", total)))
		return nil
	}
	return w.svc.SetFeedBackfill(ctx, k, model.BackfillRunning, page+1, total)
}

// clientError classifies a platform error during a poll (polling=true) or a backfill page.
func (w *Worker) clientError(ctx context.Context, p platform.Platform, wf *service.WorkFeed, err error, polling bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	k := wf.Key()
	switch {
	case errors.Is(err, platform.ErrRateLimited):
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindRateLimit, "rate limited by "+p.DisplayName+"; waiting for the limit to reset"))
		return nil
	case errors.Is(err, platform.ErrNotFound):
		msg := "player not found on " + p.DisplayName + "; tracking of this account disabled"
		if err := w.svc.MarkIdentityError(ctx, wf.PlayerID, wf.Platform, msg, true); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelError, model.KindPoll, msg))
		return nil
	}
	msg := err.Error()
	if polling {
		if err := w.svc.MarkFeedPolled(ctx, k); err != nil {
			return err
		}
		if err := w.svc.MarkFeedError(ctx, k, msg); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelError, model.KindPoll, "poll failed: "+msg))
		return nil
	}
	if err := w.svc.DeferFeedBackfill(ctx, k, w.svc.Now().Add(service.BackfillRetryDelay), msg); err != nil {
		return err
	}
	w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindBackfill, "backfill page failed, retrying in 5m: "+msg))
	return nil
}
