package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

type ReconcileResult struct {
	RemovedTmp, Adopted, Orphans, Requeued int
}

func (r ReconcileResult) Changed() bool { return r != ReconcileResult{} }

// ReconcileStorage aligns replay files with score rows (spec §6.3). Orphan
// files are kept: they may belong to a player deleted with "keep files".
func (s *Service) ReconcileStorage(ctx context.Context) (ReconcileResult, error) {
	var res ReconcileResult
	entries, removed, err := s.store.Scan()
	if err != nil {
		return res, err
	}
	res.RemovedTmp = removed
	onDisk := make(map[int64]storage.Entry, len(entries))
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		onDisk[e.ScoreID] = e
		ids = append(ids, e.ScoreID)
	}
	q := s.q.Score
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		rows, err := q.WithContext(ctx).Where(q.ID.In(chunk...)).Find()
		if err != nil {
			return res, fmt.Errorf("service: reconcile load: %w", err)
		}
		byID := make(map[int64]*model.Score, len(rows))
		for _, r := range rows {
			byID[r.ID] = r
		}
		for _, id := range chunk {
			e := onDisk[id]
			sc, ok := byID[id]
			if !ok || sc.PlayerID != e.PlayerID {
				res.Orphans++
				continue
			}
			if sc.ReplayState == model.ReplayArchived {
				continue
			}
			size, sum, err := storage.HashFile(e.Path)
			if err != nil {
				slog.Warn("reconcile: hash failed", "path", e.Path, "err", err)
				continue
			}
			if err := s.MarkReplayArchived(ctx, id, size, sum); err != nil {
				return res, err
			}
			res.Adopted++
		}
	}
	var archived []int64
	if err := q.WithContext(ctx).Where(q.ReplayState.Eq(model.ReplayArchived)).Pluck(q.ID, &archived); err != nil {
		return res, fmt.Errorf("service: reconcile archived: %w", err)
	}
	for _, id := range archived {
		if _, ok := onDisk[id]; ok {
			continue
		}
		if err := s.updateScore(ctx, id, map[string]any{
			"replay_state": model.ReplayPending, "replay_size": 0, "replay_sha256": "", "archived_at": nil,
			"attempts": 0, "next_attempt_at": nil, "last_error": "file missing on disk; re-downloading",
		}); err != nil {
			return res, err
		}
		res.Requeued++
	}
	return res, nil
}
