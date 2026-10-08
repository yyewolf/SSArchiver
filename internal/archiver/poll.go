package archiver

import (
	"context"
	"errors"
	"fmt"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func (w *Worker) poll(ctx context.Context, p *model.Player) error {
	w.setStatus(StateRunning, "Polling "+p.Name)
	var newScores, newReplays, pagesRead, totalPages int
	reachedEnd := false
	for page := 1; page <= MaxPollPages; page++ {
		sp, err := w.client.Scores(ctx, p.ID, page)
		if err != nil {
			return w.clientError(ctx, p, err, true)
		}
		pagesRead, totalPages = page, sp.Metadata.TotalPages
		if page == 1 && len(sp.Data) > 0 {
			if err := w.svc.UpdatePlayerProfile(ctx, p.ID, sp.Data[0].Score.Player); err != nil {
				return err
			}
		}
		res, err := w.svc.UpsertScores(ctx, p.ID, sp.Data)
		if err != nil {
			return err
		}
		newScores += res.New
		newReplays += res.NewReplays
		if res.Known > 0 || len(sp.Data) == 0 || page >= sp.Metadata.TotalPages {
			reachedEnd = true
			break
		}
	}
	switch {
	case p.BackfillState == model.BackfillPending && p.BackfillPage <= 1:
		if reachedEnd {
			if err := w.svc.SetBackfill(ctx, p.ID, model.BackfillDone, pagesRead+1, totalPages); err != nil {
				return err
			}
		} else if err := w.svc.SetBackfill(ctx, p.ID, model.BackfillPending, MaxPollPages+1, totalPages); err != nil {
			return err
		}
	case !reachedEnd && p.BackfillState == model.BackfillDone:
		if err := w.svc.SetBackfill(ctx, p.ID, model.BackfillPending, MaxPollPages+1, totalPages); err != nil {
			return err
		}
		w.svc.Log(ctx, model.SyncEvent{
			Level: model.LevelWarn, Kind: model.KindBackfill, PlayerID: service.Ptr(p.ID),
			Message: fmt.Sprintf("more than %d pages of new scores since the last poll; resuming backfill from page %d", MaxPollPages, MaxPollPages+1),
		})
	}
	if err := w.svc.MarkPolled(ctx, p.ID); err != nil {
		return err
	}
	if newScores > 0 {
		w.svc.Log(ctx, model.SyncEvent{
			Level: model.LevelInfo, Kind: model.KindScores, PlayerID: service.Ptr(p.ID),
			Message: fmt.Sprintf("%d new scores, %d with replays", newScores, newReplays),
		})
	}
	return nil
}

func (w *Worker) backfill(ctx context.Context, p *model.Player) error {
	page := max(p.BackfillPage, 1)
	w.setStatus(StateRunning, fmt.Sprintf("Backfilling %s · page %d", p.Name, page))
	sp, err := w.client.Scores(ctx, p.ID, page)
	if err != nil {
		return w.clientError(ctx, p, err, false)
	}
	if _, err := w.svc.UpsertScores(ctx, p.ID, sp.Data); err != nil {
		return err
	}
	total := sp.Metadata.TotalPages
	if len(sp.Data) == 0 || page >= total {
		if err := w.svc.SetBackfill(ctx, p.ID, model.BackfillDone, page+1, total); err != nil {
			return err
		}
		w.svc.Log(ctx, model.SyncEvent{
			Level: model.LevelInfo, Kind: model.KindBackfill, PlayerID: service.Ptr(p.ID),
			Message: fmt.Sprintf("backfill listing complete (%d pages)", total),
		})
		return nil
	}
	return w.svc.SetBackfill(ctx, p.ID, model.BackfillRunning, page+1, total)
}

// clientError classifies a ScoreSaber error during poll (polling=true) or backfill.
func (w *Worker) clientError(ctx context.Context, p *model.Player, err error, polling bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	pid := service.Ptr(p.ID)
	switch {
	case errors.Is(err, scoresaber.ErrRateLimited):
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelWarn, Kind: model.KindRateLimit, PlayerID: pid, Message: "rate limited by ScoreSaber; waiting for the limit to reset"})
		return nil
	case errors.Is(err, scoresaber.ErrNotFound):
		const msg = "player not found on ScoreSaber; tracking disabled"
		if err := w.svc.MarkPlayerError(ctx, p.ID, msg, true); err != nil {
			return err
		}
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindPoll, PlayerID: pid, Message: msg})
		return nil
	}
	msg := err.Error()
	if polling {
		if err := w.svc.MarkPolled(ctx, p.ID); err != nil {
			return err
		}
		if err := w.svc.MarkPlayerError(ctx, p.ID, msg, false); err != nil {
			return err
		}
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindPoll, PlayerID: pid, Message: "poll failed: " + msg})
		return nil
	}
	if err := w.svc.DeferBackfill(ctx, p.ID, w.svc.Now().Add(service.BackfillRetryDelay), msg); err != nil {
		return err
	}
	w.svc.Log(ctx, model.SyncEvent{Level: model.LevelWarn, Kind: model.KindBackfill, PlayerID: pid, Message: "backfill page failed, retrying in 5m: " + msg})
	return nil
}
