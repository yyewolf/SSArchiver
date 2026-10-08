package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
)

type ReplayTier int

const (
	TierNew      ReplayTier = iota // scores set after the player was added
	TierBackfill                   // historical scores
)

const MaxReplayAttempts = 5

var backoffSchedule = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

// Backoff is the delay after the given (1-based) failed attempt.
func Backoff(attempt int) time.Duration {
	i := min(max(attempt-1, 0), len(backoffSchedule)-1)
	return backoffSchedule[i]
}

func (s *Service) replayCandidates(ctx context.Context, tier ReplayTier) query.IScoreDo {
	q, p := s.q.Score, s.q.Player
	now := s.Now()
	do := q.WithContext(ctx).Join(p, p.ID.EqCol(q.PlayerID)).
		Where(q.ReplayState.Eq(model.ReplayPending), p.Enabled.Is(true)).
		Where(q.WithContext(ctx).Where(q.NextAttemptAt.IsNull()).Or(q.NextAttemptAt.Lte(now)))
	if tier == TierNew {
		return do.Where(q.SetAt.GteCol(p.AddedAt))
	}
	return do.Where(q.SetAt.LtCol(p.AddedAt))
}

// NextReplay returns the newest pending replay of the next player (round
// robin after lastPlayer) in the given tier, or nil when there is none.
func (s *Service) NextReplay(ctx context.Context, tier ReplayTier, lastPlayer string) (*model.Score, error) {
	q := s.q.Score
	var ids []string
	if err := s.replayCandidates(ctx, tier).Distinct(q.PlayerID).Order(q.PlayerID).Pluck(q.PlayerID, &ids); err != nil {
		return nil, fmt.Errorf("service: replay players: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	pick := pickAfter(ids, lastPlayer)
	sc, err := s.replayCandidates(ctx, tier).Select(q.ALL).Preload(q.Leaderboard, q.Player).
		Where(q.PlayerID.Eq(pick)).Order(q.SetAt.Desc()).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("service: next replay: %w", err)
	}
	return sc, nil
}

func (s *Service) updateScore(ctx context.Context, id int64, upd map[string]any) error {
	info, err := s.q.Score.WithContext(ctx).Where(s.q.Score.ID.Eq(id)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: update score %d: %w", id, err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: score %d", ErrNotFound, id)
	}
	return nil
}

func (s *Service) MarkReplayArchived(ctx context.Context, id, size int64, sha string) error {
	return s.updateScore(ctx, id, map[string]any{
		"replay_state": model.ReplayArchived, "replay_size": size, "replay_sha256": sha,
		"archived_at": s.Now(), "next_attempt_at": nil, "last_error": "",
	})
}

func (s *Service) MarkReplayGone(ctx context.Context, id int64) error {
	return s.updateScore(ctx, id, map[string]any{
		"replay_state": model.ReplayGone, "next_attempt_at": nil, "last_error": "replay no longer available on ScoreSaber",
	})
}

// MarkReplayAttemptFailed records a failed download. After MaxReplayAttempts
// it marks the score failed and returns gaveUp=true.
func (s *Service) MarkReplayAttemptFailed(ctx context.Context, id int64, cause error) (bool, time.Time, error) {
	sc, err := s.q.Score.WithContext(ctx).Where(s.q.Score.ID.Eq(id)).First()
	if err != nil {
		return false, time.Time{}, notFound(err, fmt.Sprintf("score %d", id))
	}
	attempts := sc.Attempts + 1
	msg := cause.Error()
	if attempts >= MaxReplayAttempts {
		return true, time.Time{}, s.updateScore(ctx, id, map[string]any{
			"attempts": attempts, "replay_state": model.ReplayFailed, "next_attempt_at": nil, "last_error": msg,
		})
	}
	next := s.Now().Add(Backoff(attempts))
	return false, next, s.updateScore(ctx, id, map[string]any{"attempts": attempts, "next_attempt_at": next, "last_error": msg})
}

// RetryFailed requeues failed replays, optionally limited to a player or one score.
func (s *Service) RetryFailed(ctx context.Context, playerID string, scoreID int64) (int64, error) {
	q := s.q.Score
	do := q.WithContext(ctx).Where(q.ReplayState.Eq(model.ReplayFailed))
	if playerID != "" {
		do = do.Where(q.PlayerID.Eq(playerID))
	}
	if scoreID != 0 {
		do = do.Where(q.ID.Eq(scoreID))
	}
	info, err := do.Updates(map[string]any{"replay_state": model.ReplayPending, "attempts": 0, "next_attempt_at": nil, "last_error": ""})
	if err != nil {
		return 0, fmt.Errorf("service: retry failed: %w", err)
	}
	if info.RowsAffected > 0 {
		s.Wake()
	}
	return info.RowsAffected, nil
}

func (s *Service) ListFailedReplays(ctx context.Context, page, perPage int) ([]*model.Score, int64, error) {
	page, perPage = max(page, 1), min(max(perPage, 1), 100)
	q := s.q.Score
	items, total, err := q.WithContext(ctx).Preload(q.Leaderboard, q.Player).
		Where(q.ReplayState.Eq(model.ReplayFailed)).Order(q.SetAt.Desc()).FindByPage((page-1)*perPage, perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("service: list failed: %w", err)
	}
	return items, total, nil
}

// NextRetryAt is the earliest next_attempt_at among pending replays.
func (s *Service) NextRetryAt(ctx context.Context) (time.Time, bool, error) {
	q := s.q.Score
	sc, err := q.WithContext(ctx).Where(q.ReplayState.Eq(model.ReplayPending), q.NextAttemptAt.IsNotNull()).
		Order(q.NextAttemptAt).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("service: next retry: %w", err)
	}
	return *sc.NextAttemptAt, true, nil
}
