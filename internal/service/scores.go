package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm/clause"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

const (
	FilterWithReplay = "replay"
	FilterArchived   = "archived"
)

type UpsertResult struct{ New, Known, NewReplays int }

type ScoreFilter struct {
	PlayerID   string
	Search     string
	RankedOnly bool
	State      string // "", FilterWithReplay, FilterArchived
	Page       int
	PerPage    int
}

type ScoreList struct {
	Items   []*model.Score
	Total   int64
	Page    int
	PerPage int
	Pages   int
}

// UpsertPlays stores a page of plays from one platform (spec §5.4). Existing
// rows only get their mutable ranking fields refreshed; archive columns are
// never touched, except none → pending when the platform newly offers a replay.
func (s *Service) UpsertPlays(ctx context.Context, playerID, platformName string, plays []platform.Play) (UpsertResult, error) {
	var res UpsertResult
	if len(plays) == 0 {
		return res, nil
	}
	p, ok := s.reg.Get(platformName)
	if !ok {
		return res, fmt.Errorf("service: upsert: unknown platform %q", platformName)
	}
	err := s.q.Transaction(func(tx *query.Query) error {
		alloc := &idAllocator{ctx: ctx, tx: tx, legacy: p.Legacy}
		lbIDs, err := upsertLeaderboards(ctx, tx, p.Name, plays, alloc)
		if err != nil {
			return err
		}
		byKey, err := existingPlays(ctx, tx, p.Name, plays)
		if err != nil {
			return err
		}
		var fresh []*model.Score
		for _, pl := range plays {
			key := pl.Kind + "|" + pl.ExternalID
			if old, ok := byKey[key]; ok {
				res.Known++
				if err := refreshPlay(ctx, tx, old, pl, &res); err != nil {
					return err
				}
				continue
			}
			id, err := alloc.score(pl.ExternalID)
			if err != nil {
				return err
			}
			row := playRow(playerID, p.Name, id, lbIDs[pl.Leaderboard.ExternalID], pl)
			if row.ReplayState == model.ReplayPending {
				res.NewReplays++
			}
			res.New++
			fresh = append(fresh, row)
			byKey[key] = row // guards against duplicates within one page
		}
		if len(fresh) > 0 {
			if err := tx.Score.WithContext(ctx).CreateInBatches(fresh, 100); err != nil {
				return fmt.Errorf("insert scores: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return UpsertResult{}, fmt.Errorf("service: upsert plays: %w", err)
	}
	return res, nil
}

// idAllocator hands out row IDs: legacy platforms use their own numeric IDs;
// the others get sequential internal IDs from platform.InternalIDBase
// (spec §4.5), looked up once per transaction.
type idAllocator struct {
	ctx            context.Context
	tx             *query.Query
	legacy         bool
	nextLB, nextSc int64
}

func (a *idAllocator) leaderboard(externalID string) (int64, error) {
	if a.legacy {
		return a.legacyID(externalID)
	}
	lb := a.tx.Leaderboard
	return a.next(&a.nextLB, func(out *[]int64) error {
		return lb.WithContext(a.ctx).Where(lb.ID.Gte(platform.InternalIDBase)).Order(lb.ID.Desc()).Limit(1).Pluck(lb.ID, out)
	})
}

func (a *idAllocator) score(externalID string) (int64, error) {
	if a.legacy {
		return a.legacyID(externalID)
	}
	q := a.tx.Score
	return a.next(&a.nextSc, func(out *[]int64) error {
		return q.WithContext(a.ctx).Where(q.ID.Gte(platform.InternalIDBase)).Order(q.ID.Desc()).Limit(1).Pluck(q.ID, out)
	})
}

func (a *idAllocator) next(cur *int64, maxID func(*[]int64) error) (int64, error) {
	if *cur == 0 {
		var ids []int64
		if err := maxID(&ids); err != nil {
			return 0, fmt.Errorf("service: allocate id: %w", err)
		}
		*cur = platform.InternalIDBase
		if len(ids) == 1 {
			*cur = ids[0] + 1
		}
	}
	id := *cur
	*cur++
	return id, nil
}

func (a *idAllocator) legacyID(externalID string) (int64, error) {
	id, err := strconv.ParseInt(externalID, 10, 64)
	if err != nil || id <= 0 || id >= platform.InternalIDBase {
		return 0, fmt.Errorf("service: invalid legacy id %q", externalID)
	}
	return id, nil
}

func upsertLeaderboards(ctx context.Context, tx *query.Query, platformName string, plays []platform.Play, alloc *idAllocator) (map[string]int64, error) {
	lb := tx.Leaderboard
	var ext []string
	seen := map[string]bool{}
	for _, pl := range plays {
		if e := pl.Leaderboard.ExternalID; !seen[e] {
			seen[e] = true
			ext = append(ext, e)
		}
	}
	existing, err := lb.WithContext(ctx).Where(lb.Platform.Eq(platformName), lb.ExternalID.In(ext...)).Find()
	if err != nil {
		return nil, fmt.Errorf("load leaderboards: %w", err)
	}
	ids := make(map[string]int64, len(ext))
	for _, e := range existing {
		ids[e.ExternalID] = e.ID
	}
	rows := make([]*model.Leaderboard, 0, len(ext))
	done := map[string]bool{}
	for _, pl := range plays {
		d := pl.Leaderboard
		if done[d.ExternalID] {
			continue
		}
		done[d.ExternalID] = true
		id, ok := ids[d.ExternalID]
		if !ok {
			if id, err = alloc.leaderboard(d.ExternalID); err != nil {
				return nil, err
			}
			ids[d.ExternalID] = id
		}
		rows = append(rows, &model.Leaderboard{
			ID: id, SongHash: d.SongHash, SongName: d.SongName, SongSubName: d.SongSubName, SongAuthor: d.SongAuthor,
			Mapper: d.Mapper, Difficulty: d.Difficulty, DifficultyRaw: d.DifficultyRaw, GameMode: d.GameMode,
			CoverURL: d.CoverURL, Status: d.Status, Stars: d.Stars, MaxScore: d.MaxScore,
			Platform: platformName, ExternalID: d.ExternalID, MapKey: platform.MapKey(d.SongHash, d.GameMode, d.Difficulty),
		})
	}
	if err := lb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}}, UpdateAll: true,
	}).CreateInBatches(rows, 100); err != nil {
		return nil, fmt.Errorf("upsert leaderboards: %w", err)
	}
	return ids, nil
}

func existingPlays(ctx context.Context, tx *query.Query, platformName string, plays []platform.Play) (map[string]*model.Score, error) {
	q := tx.Score
	ext := make([]string, 0, len(plays))
	for _, pl := range plays {
		ext = append(ext, pl.ExternalID)
	}
	rows, err := q.WithContext(ctx).Where(q.Platform.Eq(platformName), q.ExternalID.In(ext...)).Find()
	if err != nil {
		return nil, fmt.Errorf("load existing scores: %w", err)
	}
	out := make(map[string]*model.Score, len(rows))
	for _, r := range rows {
		out[r.Kind+"|"+r.ExternalID] = r
	}
	return out, nil
}

func refreshPlay(ctx context.Context, tx *query.Query, old *model.Score, pl platform.Play, res *UpsertResult) error {
	upd := map[string]any{"rank": pl.Rank, "pp": pl.PP, "personal_best": pl.PersonalBest, "has_replay": pl.HasReplay || old.HasReplay}
	if pl.HasReplay && old.ReplayState == model.ReplayNone {
		upd["replay_state"] = model.ReplayPending
		res.NewReplays++
	}
	if pl.ReplayURL != "" && old.ReplayState != model.ReplayArchived {
		upd["replay_url"] = pl.ReplayURL
	}
	if _, err := tx.Score.WithContext(ctx).Where(tx.Score.ID.Eq(old.ID)).Updates(upd); err != nil {
		return fmt.Errorf("update score %d: %w", old.ID, err)
	}
	return nil
}

func playRow(playerID, platformName string, id, lbID int64, pl platform.Play) *model.Score {
	state := model.ReplayNone
	if pl.HasReplay {
		state = model.ReplayPending
	}
	var url *string
	if pl.ReplayURL != "" {
		url = &pl.ReplayURL
	}
	return &model.Score{
		ID: id, PlayerID: playerID, LeaderboardID: lbID, Rank: pl.Rank,
		ModifiedScore: pl.ModifiedScore, UnmodifiedScore: pl.UnmodifiedScore, Accuracy: pl.Accuracy, PP: pl.PP,
		Mods: pl.Mods, FullCombo: pl.FullCombo, MissedNotes: pl.MissedNotes, BadCuts: pl.BadCuts,
		MaxCombo: pl.MaxCombo, HMD: pl.HMD, PersonalBest: pl.PersonalBest, SetAt: pl.SetAt.UTC(),
		HasReplay: pl.HasReplay, ReplayState: state,
		Platform: platformName, Kind: pl.Kind, EndType: pl.EndType, EndTime: pl.EndTime, ExternalID: pl.ExternalID, ReplayURL: url,
	}
}

var likeStripper = strings.NewReplacer("%", "", "_", "")

func (s *Service) ListScores(ctx context.Context, f ScoreFilter) (ScoreList, error) {
	if f.PerPage <= 0 {
		f.PerPage = 50
	}
	f.PerPage = min(f.PerPage, 100)
	f.Page = max(f.Page, 1)
	q, lb := s.q.Score, s.q.Leaderboard
	filtered := func() query.IScoreDo {
		do := q.WithContext(ctx).Join(lb, lb.ID.EqCol(q.LeaderboardID))
		if f.PlayerID != "" {
			do = do.Where(q.PlayerID.Eq(f.PlayerID))
		}
		if term := likeStripper.Replace(strings.TrimSpace(f.Search)); term != "" {
			p := "%" + term + "%"
			do = do.Where(q.WithContext(ctx).Where(lb.SongName.Like(p)).Or(lb.SongAuthor.Like(p)).Or(lb.Mapper.Like(p)))
		}
		if f.RankedOnly {
			do = do.Where(lb.Status.Eq("RANKED"))
		}
		switch f.State {
		case FilterWithReplay:
			do = do.Where(q.HasReplay.Is(true))
		case FilterArchived:
			do = do.Where(q.ReplayState.Eq(model.ReplayArchived))
		}
		return do
	}
	total, err := filtered().Count()
	if err != nil {
		return ScoreList{}, fmt.Errorf("service: count scores: %w", err)
	}
	items, err := filtered().Select(q.ALL).Preload(q.Leaderboard).
		Order(q.SetAt.Desc(), q.ID.Desc()).Offset((f.Page - 1) * f.PerPage).Limit(f.PerPage).Find()
	if err != nil {
		return ScoreList{}, fmt.Errorf("service: list scores: %w", err)
	}
	pages := int((total + int64(f.PerPage) - 1) / int64(f.PerPage))
	return ScoreList{Items: items, Total: total, Page: f.Page, PerPage: f.PerPage, Pages: max(pages, 1)}, nil
}

func (s *Service) GetScore(ctx context.Context, id int64) (*model.Score, error) {
	q := s.q.Score
	sc, err := q.WithContext(ctx).Preload(q.Leaderboard, q.Player).Where(q.ID.Eq(id)).First()
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("score %d", id))
	}
	return sc, nil
}
