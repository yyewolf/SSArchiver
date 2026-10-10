package service

import (
	"context"
	"fmt"
	"slices"
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

type UpsertResult struct{ New, Known, NewReplays, URLChanged int }

type ScoreFilter struct {
	PlayerID   string
	Search     string
	RankedOnly bool
	State      string // "", FilterWithReplay, FilterArchived
	Platform   string // "" = every platform
	MinScore   *int64 // inclusive bounds on modified_score
	MaxScore   *int64
	MapKey     string // one map (the merged page's "more plays")
	Page       int
	PerPage    int
}

func (f ScoreFilter) normalized() ScoreFilter {
	if f.PerPage <= 0 {
		f.PerPage = 50
	}
	f.PerPage = min(f.PerPage, 100)
	f.Page = max(f.Page, 1)
	return f
}

// playsFrom joins every play to its leaderboard; conditions use s and lb.
const playsFrom = " FROM scores s JOIN leaderboards lb ON lb.id = s.leaderboard_id WHERE "

// where is the per-play condition shared by the row listing and the merged
// map view: a filter always means the same thing in both.
func (f ScoreFilter) where() (string, []any) {
	conds := []string{"1 = 1"}
	var args []any
	add := func(cond string, a ...any) {
		conds = append(conds, cond)
		args = append(args, a...)
	}
	if f.PlayerID != "" {
		add("s.player_id = ?", f.PlayerID)
	}
	if term := likeStripper.Replace(strings.TrimSpace(f.Search)); term != "" {
		p := "%" + term + "%"
		add("(lb.song_name LIKE ? OR lb.song_author LIKE ? OR lb.mapper LIKE ?)", p, p, p)
	}
	if f.RankedOnly {
		add("lb.status = ?", "RANKED")
	}
	switch f.State {
	case FilterWithReplay:
		add("s.has_replay")
	case FilterArchived:
		add("s.replay_state = ?", model.ReplayArchived)
	}
	if f.Platform != "" {
		add("s.platform = ?", f.Platform)
	}
	if f.MinScore != nil {
		add("s.modified_score >= ?", *f.MinScore)
	}
	if f.MaxScore != nil {
		add("s.modified_score <= ?", *f.MaxScore)
	}
	if f.MapKey != "" {
		add("lb.map_key = ?", f.MapKey)
	}
	return strings.Join(conds, " AND "), args
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
		if p.PBOnly {
			if err := supersede(ctx, tx, fresh); err != nil {
				return err
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

// supersede clears personal_best on a player's older scores of the same
// leaderboard when a PB-only platform lists a new score there (spec §4.6).
func supersede(ctx context.Context, tx *query.Query, fresh []*model.Score) error {
	q := tx.Score
	for _, r := range fresh {
		if r.Kind != model.KindScore {
			continue
		}
		if _, err := q.WithContext(ctx).Where(
			q.PlayerID.Eq(r.PlayerID), q.Platform.Eq(r.Platform), q.Kind.Eq(model.KindScore),
			q.LeaderboardID.Eq(r.LeaderboardID), q.ID.Neq(r.ID), q.SetAt.Lt(r.SetAt), q.PersonalBest.Is(true),
		).Update(q.PersonalBest, false); err != nil {
			return fmt.Errorf("supersede scores: %w", err)
		}
	}
	return nil
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
	if pl.ReplayURL != "" && old.ReplayState == model.ReplayArchived && old.ReplayURL != nil && *old.ReplayURL != pl.ReplayURL {
		res.URLChanged++ // the archive is never replaced (spec §5.4); the worker logs it
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
	f = f.normalized()
	where, args := f.where()
	gdb := s.db.WithContext(ctx)
	var total int64
	if err := gdb.Raw("SELECT COUNT(*)"+playsFrom+where, args...).Scan(&total).Error; err != nil {
		return ScoreList{}, fmt.Errorf("service: count scores: %w", err)
	}
	var ids []int64
	if err := gdb.Raw("SELECT s.id"+playsFrom+where+" ORDER BY s.set_at DESC, s.id DESC LIMIT ? OFFSET ?",
		slices.Concat(args, []any{f.PerPage, (f.Page - 1) * f.PerPage})...).Scan(&ids).Error; err != nil {
		return ScoreList{}, fmt.Errorf("service: list scores: %w", err)
	}
	items, err := s.loadScores(ctx, ids)
	if err != nil {
		return ScoreList{}, err
	}
	pages := int((total + int64(f.PerPage) - 1) / int64(f.PerPage))
	return ScoreList{Items: items, Total: total, Page: f.Page, PerPage: f.PerPage, Pages: max(pages, 1)}, nil
}

// loadScores loads rows with their leaderboard, in the order of ids
// (duplicates allowed).
func (s *Service) loadScores(ctx context.Context, ids []int64) ([]*model.Score, error) {
	if len(ids) == 0 {
		return []*model.Score{}, nil
	}
	q := s.q.Score
	rows, err := q.WithContext(ctx).Preload(q.Leaderboard).Where(q.ID.In(ids...)).Find()
	if err != nil {
		return nil, fmt.Errorf("service: load scores: %w", err)
	}
	byID := make(map[int64]*model.Score, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	out := make([]*model.Score, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Service) GetScore(ctx context.Context, id int64) (*model.Score, error) {
	q := s.q.Score
	sc, err := q.WithContext(ctx).Preload(q.Leaderboard, q.Player).Where(q.ID.Eq(id)).First()
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("score %d", id))
	}
	return sc, nil
}
