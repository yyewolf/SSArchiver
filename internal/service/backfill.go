package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

const BackfillRetryDelay = 5 * time.Minute

func (s *Service) updatePlayer(ctx context.Context, id string, upd map[string]any) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: update player %s: %w", id, err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	return nil
}

// DuePlayer returns the enabled player whose poll is most overdue, or nil.
func (s *Service) DuePlayer(ctx context.Context, interval time.Duration) (*model.Player, error) {
	p := s.q.Player
	cutoff := s.Now().Add(-interval)
	pl, err := p.WithContext(ctx).Where(p.Enabled.Is(true)).
		Where(p.WithContext(ctx).Where(p.LastPolledAt.IsNull()).Or(p.LastPolledAt.Lte(cutoff))).
		Order(p.LastPolledAt, p.ID).First() // SQLite sorts NULL first
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("service: due player: %w", err)
	}
	return pl, nil
}

// NextPollAt is when the next enabled player becomes due (ok=false: no enabled players).
func (s *Service) NextPollAt(ctx context.Context, interval time.Duration) (time.Time, bool, error) {
	p := s.q.Player
	pl, err := p.WithContext(ctx).Where(p.Enabled.Is(true)).Order(p.LastPolledAt).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("service: next poll: %w", err)
	}
	if pl.LastPolledAt == nil {
		return s.Now(), true, nil
	}
	return pl.LastPolledAt.Add(interval), true, nil
}

func (s *Service) MarkPolled(ctx context.Context, id string) error {
	return s.updatePlayer(ctx, id, map[string]any{"last_polled_at": s.Now(), "last_error": ""})
}

func (s *Service) MarkPlayerError(ctx context.Context, id, msg string, disable bool) error {
	upd := map[string]any{"last_error": msg}
	if disable {
		upd["enabled"] = false
	}
	return s.updatePlayer(ctx, id, upd)
}

// UpdatePlayerProfile refreshes name/avatar/country from score payloads; empty values are ignored.
func (s *Service) UpdatePlayerProfile(ctx context.Context, id string, sp scoresaber.Player) error {
	upd := map[string]any{}
	if sp.Name != "" {
		upd["name"] = sp.Name
	}
	if sp.Avatar != "" {
		upd["avatar_url"] = sp.Avatar
	}
	if sp.Country != "" {
		upd["country"] = sp.Country
	}
	if len(upd) == 0 {
		return nil
	}
	return s.updatePlayer(ctx, id, upd)
}

// NextBackfillPlayer picks (round robin after lastPlayer) an enabled, already
// polled player whose backfill is not done and not deferred.
func (s *Service) NextBackfillPlayer(ctx context.Context, lastPlayer string) (*model.Player, error) {
	p := s.q.Player
	players, err := p.WithContext(ctx).
		Where(p.Enabled.Is(true), p.BackfillState.Neq(model.BackfillDone), p.LastPolledAt.IsNotNull()).
		Where(p.WithContext(ctx).Where(p.BackfillRetryAt.IsNull()).Or(p.BackfillRetryAt.Lte(s.Now()))).
		Order(p.ID).Find()
	if err != nil {
		return nil, fmt.Errorf("service: next backfill: %w", err)
	}
	if len(players) == 0 {
		return nil, nil
	}
	ids := make([]string, len(players))
	for i, pl := range players {
		ids[i] = pl.ID
	}
	pick := pickAfter(ids, lastPlayer)
	for _, pl := range players {
		if pl.ID == pick {
			return pl, nil
		}
	}
	return nil, nil
}

func (s *Service) SetBackfill(ctx context.Context, id, state string, nextPage, totalPages int) error {
	return s.updatePlayer(ctx, id, map[string]any{
		"backfill_state": state, "backfill_page": nextPage, "backfill_total_pages": totalPages, "backfill_retry_at": nil,
	})
}

func (s *Service) DeferBackfill(ctx context.Context, id string, until time.Time, msg string) error {
	return s.updatePlayer(ctx, id, map[string]any{"backfill_retry_at": until, "last_error": msg})
}
