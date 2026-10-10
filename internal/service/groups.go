package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// MapGroup is one row of the merged player page (spec §6.1): the plays of one
// map (map_key) that match the filter.
type MapGroup struct {
	MapKey string
	Latest *model.Score   // the newest matching play; shows the map
	Chips  []*model.Score // each platform's current personal best, primary platform first
	Plays  int64          // matching plays of this map
}

// More is how many matching plays the chips do not show.
func (g MapGroup) More() int64 { return max(g.Plays-int64(len(g.Chips)), 0) }

type MapGroupList struct {
	Groups  []MapGroup
	Total   int64 // matching maps
	Page    int
	PerPage int
	Pages   int
}

// ListMapGroups pages over a player's maps, newest play first. Grouping,
// ordering and paging happen in SQL; a map is listed when any of its plays
// matches f. Chips are each platform's current personal best on that map
// (restricted to f.Platform when set), whatever the other filters.
func (s *Service) ListMapGroups(ctx context.Context, f ScoreFilter) (MapGroupList, error) {
	if f.PlayerID == "" {
		return MapGroupList{}, errors.New("service: map groups need a player")
	}
	f = f.normalized()
	where, args := f.where()
	gdb := s.db.WithContext(ctx)
	out := MapGroupList{Page: f.Page, PerPage: f.PerPage}
	if err := gdb.Raw("SELECT COUNT(DISTINCT lb.map_key)"+playsFrom+where, args...).Scan(&out.Total).Error; err != nil {
		return out, fmt.Errorf("service: count maps: %w", err)
	}
	out.Pages = max(int((out.Total+int64(f.PerPage)-1)/int64(f.PerPage)), 1)

	type groupRow struct {
		MapKey string
		Plays  int64
	}
	var rows []groupRow
	if err := gdb.Raw("SELECT lb.map_key AS map_key, COUNT(*) AS plays"+playsFrom+where+
		" GROUP BY lb.map_key ORDER BY MAX(s.set_at) DESC, lb.map_key LIMIT ? OFFSET ?",
		slices.Concat(args, []any{f.PerPage, (f.Page - 1) * f.PerPage})...).Scan(&rows).Error; err != nil {
		return out, fmt.Errorf("service: list maps: %w", err)
	}
	if len(rows) == 0 {
		return out, nil
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.MapKey
	}

	var latestIDs []int64
	if err := gdb.Raw("SELECT id FROM (SELECT s.id AS id, ROW_NUMBER() OVER "+
		"(PARTITION BY lb.map_key ORDER BY s.set_at DESC, s.id DESC) AS rn"+playsFrom+where+" AND lb.map_key IN ?) WHERE rn = 1",
		slices.Concat(args, []any{keys})...).Scan(&latestIDs).Error; err != nil {
		return out, fmt.Errorf("service: newest plays: %w", err)
	}
	bestWhere := "s.player_id = ? AND s.kind = ? AND s.personal_best AND lb.map_key IN ?"
	chipArgs := []any{f.PlayerID, model.KindScore, keys}
	if f.Platform != "" {
		bestWhere += " AND s.platform = ?"
		chipArgs = append(chipArgs, f.Platform)
	}
	var chipIDs []int64
	if err := gdb.Raw("SELECT id FROM (SELECT s.id AS id, ROW_NUMBER() OVER "+
		"(PARTITION BY lb.map_key, s.platform ORDER BY s.modified_score DESC, s.set_at DESC, s.id DESC) AS rn"+
		playsFrom+bestWhere+") WHERE rn = 1", chipArgs...).Scan(&chipIDs).Error; err != nil {
		return out, fmt.Errorf("service: personal bests: %w", err)
	}

	loaded, err := s.loadScores(ctx, slices.Concat(latestIDs, chipIDs))
	if err != nil {
		return out, err
	}
	byID := make(map[int64]*model.Score, len(loaded))
	for _, sc := range loaded {
		byID[sc.ID] = sc
	}
	latest := map[string]*model.Score{}
	for _, id := range latestIDs {
		if sc := byID[id]; sc != nil && sc.Leaderboard != nil {
			latest[sc.Leaderboard.MapKey] = sc
		}
	}
	chips := map[string][]*model.Score{}
	for _, id := range chipIDs {
		if sc := byID[id]; sc != nil && sc.Leaderboard != nil {
			chips[sc.Leaderboard.MapKey] = append(chips[sc.Leaderboard.MapKey], sc)
		}
	}
	for _, r := range rows {
		c := chips[r.MapKey]
		slices.SortFunc(c, func(a, b *model.Score) int {
			return cmp.Or(cmp.Compare(s.platformOrder(a.Platform), s.platformOrder(b.Platform)), strings.Compare(a.Platform, b.Platform))
		})
		out.Groups = append(out.Groups, MapGroup{MapKey: r.MapKey, Latest: latest[r.MapKey], Chips: c, Plays: r.Plays})
	}
	return out, nil
}

// GetPlay finds a row by its platform, kind and platform ID (spec §6.1).
func (s *Service) GetPlay(ctx context.Context, platformName, kind, externalID string) (*model.Score, error) {
	q := s.q.Score
	sc, err := q.WithContext(ctx).Preload(q.Leaderboard, q.Player).
		Where(q.Platform.Eq(platformName), q.Kind.Eq(kind), q.ExternalID.Eq(externalID)).First()
	if err != nil {
		return nil, notFound(err, platformName+" "+kind+" "+externalID)
	}
	return sc, nil
}
