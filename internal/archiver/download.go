package archiver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

func describe(sc *model.Score) string {
	player, song := sc.PlayerID, "?"
	if sc.Player != nil {
		player = sc.Player.Name
	}
	if sc.Leaderboard != nil {
		song = sc.Leaderboard.SongName
	}
	return fmt.Sprintf("Downloading replay %d · %s · %s", sc.ID, player, song)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func (w *Worker) download(ctx context.Context, sc *model.Score) error {
	w.setStatus(StateRunning, describe(sc))
	ev := func(level, msg string) {
		w.svc.Log(ctx, model.SyncEvent{
			Level: level, Kind: model.KindReplay, PlayerID: new(sc.PlayerID), ScoreID: new(sc.ID),
			Platform: new(sc.Platform), Feed: new(sc.Kind), Message: msg,
		})
	}
	p, err := w.platform(sc.Platform)
	if err != nil {
		return err
	}
	ref := platform.ReplayRef{Kind: sc.Kind, ExternalID: sc.ExternalID}
	if sc.ReplayURL != nil {
		ref.URL = *sc.ReplayURL
	}
	body, err := p.Adapter.Replay(ctx, ref)
	if err == nil {
		size, sum, perr := w.svc.PutReplay(sc, body)
		_ = body.Close()
		if perr == nil {
			if err := w.svc.MarkReplayArchived(ctx, sc.ID, size, sum); err != nil {
				return err
			}
			ev(model.LevelInfo, "archived replay ("+humanBytes(size)+")")
			return nil
		}
		if errors.Is(perr, storage.ErrWrite) {
			if err := w.svc.SetWorkerPaused(ctx, true); err != nil {
				return err
			}
			w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindWorker, Message: "storage error, worker paused: " + perr.Error()})
			return nil
		}
		err = perr
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, platform.ErrRateLimited):
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelWarn, Kind: model.KindRateLimit, Message: "rate limited by " + p.DisplayName + "; waiting for the limit to reset"})
		return nil
	case errors.Is(err, platform.ErrUnauthorized) && needsAccess(p, sc.Kind):
		if err := w.svc.MarkFeedPrivate(ctx, service.FeedKey{PlayerID: sc.PlayerID, Platform: sc.Platform, Kind: sc.Kind}); err != nil {
			return err
		}
		ev(model.LevelWarn, accessLostMessage(p, sc.Kind)) // the replay stays pending, no attempt is counted
		return nil
	case errors.Is(err, platform.ErrNotFound):
		if err := w.svc.MarkReplayGone(ctx, sc.ID, "replay no longer available on "+p.DisplayName); err != nil {
			return err
		}
		ev(model.LevelWarn, "replay no longer available on "+p.DisplayName)
		return nil
	}
	gaveUp, next, merr := w.svc.MarkReplayAttemptFailed(ctx, sc.ID, err)
	if merr != nil {
		return merr
	}
	if gaveUp {
		ev(model.LevelError, fmt.Sprintf("giving up after %d attempts: %v", service.MaxReplayAttempts, err))
	} else {
		ev(model.LevelWarn, fmt.Sprintf("download failed (%v); retrying at %s", err, next.Format(time.RFC3339)))
	}
	return nil
}
