package service

import (
	"context"
	"log/slog"

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
}
