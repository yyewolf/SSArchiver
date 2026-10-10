package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// Log records a sync event in the DB and in the process log. Failures to
// store are logged and otherwise ignored.
func (s *Service) Log(ctx context.Context, e model.SyncEvent) {
	if e.At.IsZero() {
		e.At = s.Now()
	}
	level := slog.LevelInfo
	switch e.Level {
	case model.LevelWarn:
		level = slog.LevelWarn
	case model.LevelError:
		level = slog.LevelError
	}
	attrs := []any{"kind", e.Kind}
	if e.PlayerID != nil {
		attrs = append(attrs, "player", *e.PlayerID)
	}
	if e.ScoreID != nil {
		attrs = append(attrs, "score", *e.ScoreID)
	}
	slog.Log(ctx, level, e.Message, attrs...)
	if err := s.q.SyncEvent.WithContext(ctx).Create(&e); err != nil {
		slog.Warn("sync event not stored", "err", err)
	}
	// Every user-visible change funnels through Log, so it is the one place
	// the live UI learns that counters or feeds moved.
	var player string
	if e.PlayerID != nil {
		player = *e.PlayerID
	}
	s.Publish(Update{PlayerID: player, Kind: e.Kind})
}

const (
	EventRetention = 7 * 24 * time.Hour
	MaxEvents      = 10000
)

type EventFilter struct {
	Level, Kind, PlayerID, Platform, Feed string
	Page, PerPage                         int
}

func (s *Service) ListEvents(ctx context.Context, f EventFilter) ([]*model.SyncEvent, int64, error) {
	if f.PerPage <= 0 {
		f.PerPage = 50
	}
	f.PerPage = min(f.PerPage, 200)
	f.Page = max(f.Page, 1)
	e := s.q.SyncEvent
	do := e.WithContext(ctx)
	if f.Level != "" {
		do = do.Where(e.Level.Eq(f.Level))
	}
	if f.Kind != "" {
		do = do.Where(e.Kind.Eq(f.Kind))
	}
	if f.PlayerID != "" {
		do = do.Where(e.PlayerID.Eq(f.PlayerID))
	}
	if f.Platform != "" {
		do = do.Where(e.Platform.Eq(f.Platform))
	}
	if f.Feed != "" {
		do = do.Where(e.Feed.Eq(f.Feed))
	}
	items, total, err := do.Order(e.ID.Desc()).FindByPage((f.Page-1)*f.PerPage, f.PerPage)
	if err != nil {
		return nil, 0, fmt.Errorf("service: list events: %w", err)
	}
	return items, total, nil
}

// PruneEvents keeps the last EventRetention and at most MaxEvents rows.
func (s *Service) PruneEvents(ctx context.Context) (int64, error) {
	return s.pruneEvents(ctx, EventRetention, MaxEvents)
}

func (s *Service) pruneEvents(ctx context.Context, maxAge time.Duration, maxCount int) (int64, error) {
	e := s.q.SyncEvent
	info, err := e.WithContext(ctx).Where(e.At.Lt(s.Now().Add(-maxAge))).Delete()
	if err != nil {
		return 0, fmt.Errorf("service: prune events by age: %w", err)
	}
	removed := info.RowsAffected
	var cut []int64
	if err := e.WithContext(ctx).Order(e.ID.Desc()).Offset(maxCount).Limit(1).Pluck(e.ID, &cut); err != nil {
		return removed, fmt.Errorf("service: prune events cutoff: %w", err)
	}
	if len(cut) == 1 {
		info, err := e.WithContext(ctx).Where(e.ID.Lte(cut[0])).Delete()
		if err != nil {
			return removed, fmt.Errorf("service: prune events by count: %w", err)
		}
		removed += info.RowsAffected
	}
	return removed, nil
}
