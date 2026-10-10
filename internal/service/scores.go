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
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
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

func leaderboardFrom(lb scoresaber.Leaderboard) *model.Leaderboard {
	return &model.Leaderboard{
		ID: lb.ID, SongHash: lb.Map.Hash, SongName: lb.Map.SongName, SongSubName: lb.Map.SongSubName,
		SongAuthor: lb.Map.SongAuthorName, Mapper: lb.Map.LevelAuthorName,
		Difficulty: lb.Difficulty.Difficulty, DifficultyRaw: lb.Difficulty.RawDifficulty, GameMode: lb.Difficulty.GameMode,
		CoverURL: lb.Map.CoverURL, Status: lb.Realm.LeaderboardStatus, Stars: lb.Realm.Stars, MaxScore: lb.MaxScore,
		Platform: model.PlatformScoreSaber, ExternalID: strconv.FormatInt(lb.ID, 10),
		MapKey: platform.MapKey(lb.Map.Hash, lb.Difficulty.GameMode, lb.Difficulty.Difficulty),
	}
}

func scoreFrom(playerID string, it scoresaber.ScoreItem) *model.Score {
	sc := it.Score
	state := model.ReplayNone
	if sc.HasReplay {
		state = model.ReplayPending
	}
	return &model.Score{
		ID: sc.ID, PlayerID: playerID, LeaderboardID: it.Leaderboard.ID, Rank: sc.Rank,
		ModifiedScore: sc.ModifiedScore, UnmodifiedScore: sc.UnmodifiedScore, Accuracy: sc.Accuracy, PP: sc.PP,
		Mods: strings.Join(sc.Mods, ","), FullCombo: sc.FullCombo, MissedNotes: sc.MissedNotes, BadCuts: sc.BadCuts,
		MaxCombo: sc.MaxCombo, HMD: sc.Device.HMD, PersonalBest: sc.PersonalBest, SetAt: sc.CreatedAt.UTC(),
		HasReplay: sc.HasReplay, ReplayState: state,
		Platform: model.PlatformScoreSaber, Kind: model.KindScore, EndType: model.EndClear, ExternalID: strconv.FormatInt(sc.ID, 10),
	}
}

// UpsertScores stores a page of ScoreSaber scores. Existing scores only get
// their mutable ranking fields refreshed; archive columns are never touched,
// except none → pending when ScoreSaber newly offers a replay.
func (s *Service) UpsertScores(ctx context.Context, playerID string, items []scoresaber.ScoreItem) (UpsertResult, error) {
	var res UpsertResult
	if len(items) == 0 {
		return res, nil
	}
	err := s.q.Transaction(func(tx *query.Query) error {
		seen := map[int64]bool{}
		var lbs []*model.Leaderboard
		ids := make([]int64, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.Score.ID)
			if !seen[it.Leaderboard.ID] {
				seen[it.Leaderboard.ID] = true
				lbs = append(lbs, leaderboardFrom(it.Leaderboard))
			}
		}
		if err := tx.Leaderboard.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}}, UpdateAll: true,
		}).CreateInBatches(lbs, 100); err != nil {
			return fmt.Errorf("upsert leaderboards: %w", err)
		}
		existing, err := tx.Score.WithContext(ctx).Where(tx.Score.ID.In(ids...)).Find()
		if err != nil {
			return fmt.Errorf("load existing scores: %w", err)
		}
		byID := make(map[int64]*model.Score, len(existing))
		for _, e := range existing {
			byID[e.ID] = e
		}
		var fresh []*model.Score
		for _, it := range items {
			sc := it.Score
			old, ok := byID[sc.ID]
			if !ok {
				row := scoreFrom(playerID, it)
				if row.ReplayState == model.ReplayPending {
					res.NewReplays++
				}
				res.New++
				fresh = append(fresh, row)
				byID[sc.ID] = row // guards against duplicates within one page
				continue
			}
			res.Known++
			upd := map[string]any{"rank": sc.Rank, "pp": sc.PP, "personal_best": sc.PersonalBest, "has_replay": sc.HasReplay || old.HasReplay}
			if sc.HasReplay && old.ReplayState == model.ReplayNone {
				upd["replay_state"] = model.ReplayPending
				res.NewReplays++
			}
			if _, err := tx.Score.WithContext(ctx).Where(tx.Score.ID.Eq(sc.ID)).Updates(upd); err != nil {
				return fmt.Errorf("update score %d: %w", sc.ID, err)
			}
		}
		if len(fresh) > 0 {
			if err := tx.Score.WithContext(ctx).CreateInBatches(fresh, 100); err != nil {
				return fmt.Errorf("insert scores: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return UpsertResult{}, fmt.Errorf("service: upsert scores: %w", err)
	}
	return res, nil
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
