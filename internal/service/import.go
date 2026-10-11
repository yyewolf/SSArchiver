package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/bsor"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// Import statuses of one uploaded replay file (spec §5).
const (
	ImportStatusImported = "imported"
	ImportStatusAlready  = "already"
	ImportStatusSkipped  = "skipped"
)

// ImportResult is the outcome of one uploaded replay file (spec §5).
type ImportResult struct {
	File     string
	Status   string // ImportStatus*
	Reason   string // skip reason; "already archived" on Status already; empty when imported
	PlayerID string // owning player's ID when the file routed to one
	ScoreID  int64  // the row the file landed on
	Map      string // "SongName [Difficulty]" for imported files
	URL      string // public page path for imported files
}

// ImportReplay archives one uploaded .bsor file into the replay-less row that
// matches it (spec §5): parse the header, route to the tracked BeatLeader
// account, match a candidate row, store the file, mark the row with a guarded
// update. Every per-file failure becomes a skipped result with a specific
// reason; the error return is reserved for fatal conditions (cancelled
// context).
func (s *Service) ImportReplay(ctx context.Context, name string, r io.Reader) (ImportResult, error) {
	res := ImportResult{File: name}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	var head bytes.Buffer
	b := bufio.NewReader(io.TeeReader(r, &head))
	h, err := bsor.ParseHeader(b)
	if err != nil {
		return skip(res, "not a BSOR replay"), nil
	}
	pp, q, lb := s.q.PlayerPlatform, s.q.Score, s.q.Leaderboard
	acct, err := pp.WithContext(ctx).Where(
		pp.Platform.Eq(model.PlatformBeatLeader), pp.ExternalID.Eq(h.PlayerID),
	).First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return skip(res, "no tracked player for "+h.PlayerID), nil
		}
		if fatal(err) {
			return res, err
		}
		return skip(res, err.Error()), nil
	}
	res.PlayerID = acct.PlayerID
	lbs, err := lb.WithContext(ctx).Where(
		lb.SongHash.Lower().Eq(strings.ToLower(h.Hash)),
		lb.GameMode.Eq(h.Mode), lb.DifficultyRaw.Eq(h.Difficulty), lb.Platform.Eq(model.PlatformBeatLeader),
	).Find()
	if err != nil {
		if fatal(err) {
			return res, err
		}
		return skip(res, err.Error()), nil
	}
	if len(lbs) == 0 {
		return skip(res, "map not in the archive"), nil
	}
	ids := make([]int64, len(lbs))
	for i, l := range lbs {
		ids[i] = l.ID
	}
	rows, err := q.WithContext(ctx).Preload(q.Leaderboard).
		Where(q.PlayerID.Eq(acct.PlayerID), q.LeaderboardID.In(ids...)).Find()
	if err != nil {
		if fatal(err) {
			return res, err
		}
		return skip(res, err.Error()), nil
	}
	var archived []*model.Score
	var pending int
	var cands []*model.Score
	for _, row := range rows {
		switch row.ReplayState {
		case model.ReplayNone, model.ReplayGone, model.ReplayFailed:
			cands = append(cands, row)
		case model.ReplayArchived:
			archived = append(archived, row)
		default:
			pending++
		}
	}
	if len(cands) == 0 {
		if len(archived) > 0 {
			res.Status, res.Reason = ImportStatusAlready, "already archived"
			return res, nil
		}
		if pending > 0 {
			return skip(res, "download in progress"), nil
		}
		return skip(res, noMatchReason(h, name)), nil
	}
	matches := matchCandidates(cands, h, name)
	if len(matches) == 0 {
		// No replay-less row matches, but the file's own play may already be
		// archived among the other rows (spec §5 step 5).
		if len(matchCandidates(archived, h, name)) > 0 {
			res.Status, res.Reason = ImportStatusAlready, "already archived"
			return res, nil
		}
		return skip(res, noMatchReason(h, name)), nil
	}
	if len(matches) > 1 {
		return skip(res, fmt.Sprintf("ambiguous: %d matching plays", len(matches))), nil
	}
	row := matches[0]
	// ParseHeader consumes the info section through its own bufio and
	// discards that bufio's read-ahead; the tee captured every byte read
	// from r so far, so replaying head ahead of r gives PutReplay the exact
	// original stream (b's own leftover buffer is a subset of head).
	size, sum, err := s.PutReplay(row, io.MultiReader(&head, r))
	if err != nil {
		if fatal(err) {
			return res, err
		}
		return skip(res, err.Error()), nil
	}
	ok, err := s.markReplayImported(ctx, row, size, sum)
	if err != nil {
		if fatal(err) {
			return res, err
		}
		return skip(res, err.Error()), nil
	}
	if !ok {
		s.removeUnreferencedImport(ctx, row)
		res.Status, res.Reason = ImportStatusAlready, "already archived"
		return res, nil
	}
	res.Status = ImportStatusImported
	res.ScoreID = row.ID
	res.Map = row.Leaderboard.SongName + " [" + row.Leaderboard.DifficultyRaw + "]"
	res.URL = s.reg.PlayPath("/s", platform.PlayRef{Platform: model.PlatformBeatLeader, Kind: row.Kind, ExternalID: row.ExternalID})
	return res, nil
}

// fileEndType resolves the file's end type (spec §5 step 5): the filename
// token, else fail when the header reports a fail time, else none.
func fileEndType(h bsor.Header, name string) string {
	if et, ok := bsor.FilenameEndType(name); ok {
		return et
	}
	if h.FailTime > 0 {
		return model.EndFail
	}
	return ""
}

// matchCandidates narrows candidates by the file's end type and the exact
// score (spec §5 step 5): fail files need a failed attempt ending within a
// second of the header's fail time, named end types need an attempt of that
// type, unknown files accept score rows and attempts of any end type. Every
// branch requires the exact score; ties are not broken.
func matchCandidates(cands []*model.Score, h bsor.Header, name string) []*model.Score {
	et := fileEndType(h, name)
	var out []*model.Score
	for _, row := range cands {
		if row.UnmodifiedScore != h.Score {
			continue
		}
		switch et {
		case "":
			out = append(out, row)
		case model.EndFail:
			if row.Kind == model.KindAttempt && row.EndType == model.EndFail && row.EndTime != nil &&
				math.Abs(*row.EndTime-h.FailTime) <= 1.0 {
				out = append(out, row)
			}
		default:
			if row.Kind == model.KindAttempt && row.EndType == et {
				out = append(out, row)
			}
		}
	}
	return out
}

// noMatchReason is the skip reason when no candidate row matches (spec §5
// step 5).
func noMatchReason(h bsor.Header, name string) string {
	et := fileEndType(h, name)
	if et == "" {
		et = "unknown"
	}
	return fmt.Sprintf("no matching play for %s [%s, %s, score %d]", h.SongName, h.Difficulty, et, h.Score)
}

// markReplayImported is the import's guarded archive mark (spec §5 step 6):
// it only lands while the row is in a replay-less state; ok is false when the
// row was archived by someone else in the meantime.
func (s *Service) markReplayImported(ctx context.Context, sc *model.Score, size int64, sha string) (bool, error) {
	q := s.q.Score
	info, err := q.WithContext(ctx).
		Where(q.ID.Eq(sc.ID), q.ReplayState.In(model.ReplayNone, model.ReplayGone, model.ReplayFailed)).
		Updates(map[string]any{
			"replay_state": model.ReplayArchived, "replay_size": size, "replay_sha256": sha,
			"archived_at": s.Now(), "next_attempt_at": nil, "last_error": "",
		})
	if err != nil {
		return false, fmt.Errorf("service: mark replay imported: %w", err)
	}
	return info.RowsAffected > 0, nil
}

// removeUnreferencedImport is the import's guard-race cleanup (spec §5
// step 6): when the guarded mark loses the race, the file just stored at the
// row's path is removed only while the fresh row does not reference it; an
// archived row owns whatever sits at its path, and a gone row has no file
// left to remove.
func (s *Service) removeUnreferencedImport(ctx context.Context, sc *model.Score) {
	fresh, err := s.q.Score.WithContext(ctx).Where(s.q.Score.ID.Eq(sc.ID)).First()
	if err != nil || fresh.ReplayState == model.ReplayArchived {
		return
	}
	if loc, lerr := s.replayLoc(sc); lerr == nil {
		_ = s.store.Remove(loc)
	}
}

// fatal reports whether err is a context failure: the one fatal condition
// ImportReplay returns instead of a skipped result (spec §5).
func fatal(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// skip fills res's skipped status (spec §5).
func skip(res ImportResult, reason string) ImportResult {
	res.Status, res.Reason = ImportStatusSkipped, reason
	return res
}
