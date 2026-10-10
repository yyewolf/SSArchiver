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
	onDisk := make(map[string]storage.Entry, len(entries))
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		onDisk[e.Path] = e
		ids = append(ids, e.RowID)
	}
	q := s.q.Score
	matched := map[string]bool{}
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		rows, err := q.WithContext(ctx).Where(q.ID.In(chunk...)).Find()
		if err != nil {
			return res, fmt.Errorf("service: reconcile load: %w", err)
		}
		for _, sc := range rows {
			path, err := s.ReplayPath(sc)
			if err != nil {
				continue // row of a platform that is no longer registered
			}
			e, ok := onDisk[path]
			if !ok {
				continue
			}
			matched[path] = true
			if sc.ReplayState == model.ReplayArchived {
				continue
			}
			size, sum, err := storage.HashFile(e.Path)
			if err != nil {
				slog.Warn("reconcile: hash failed", "path", e.Path, "err", err)
				continue
			}
			if err := s.MarkReplayArchived(ctx, sc.ID, size, sum); err != nil {
				return res, err
			}
			res.Adopted++
		}
	}
	res.Orphans = len(onDisk) - len(matched)
	archived, err := q.WithContext(ctx).Where(q.ReplayState.Eq(model.ReplayArchived)).Find()
	if err != nil {
		return res, fmt.Errorf("service: reconcile archived: %w", err)
	}
	for _, sc := range archived {
		path, err := s.ReplayPath(sc)
		if err != nil {
			continue
		}
		if _, ok := onDisk[path]; ok {
			continue
		}
		if err := s.updateScore(ctx, sc.ID, map[string]any{
			"replay_state": model.ReplayPending, "replay_size": 0, "replay_sha256": "", "archived_at": nil,
			"attempts": 0, "next_attempt_at": nil, "last_error": "file missing on disk; re-downloading",
		}); err != nil {
			return res, err
		}
		res.Requeued++
	}
	return res, nil
}
