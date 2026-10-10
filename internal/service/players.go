package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

var playerURLRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?scoresaber\.com/u/([0-9]{1,32})(?:[/?#].*)?$`)

// ParsePlayerRef extracts a player id from an id or a ScoreSaber profile URL.
func ParsePlayerRef(input string) (string, error) {
	in := strings.TrimSpace(input)
	if storage.ValidPlayerID(in) {
		return in, nil
	}
	if m := playerURLRe.FindStringSubmatch(in); m != nil {
		return m[1], nil
	}
	return "", ErrInvalidPlayerRef
}

type Counts struct {
	Scores, Archived, Pending, Failed, Gone int64
}

// Replays is the number of scores ScoreSaber offered a replay for.
func (c Counts) Replays() int64 { return c.Archived + c.Pending + c.Failed + c.Gone }

// Identity is one linked platform account with its feeds.
type Identity struct {
	model.PlayerPlatform
	Feeds []model.SyncFeed
}

// Feed returns the account's feed of the given kind (zero value when missing).
func (i Identity) Feed(kind string) model.SyncFeed {
	for _, f := range i.Feeds {
		if f.Feed == kind {
			return f
		}
	}
	return model.SyncFeed{}
}

type PlayerSummary struct {
	model.Player
	Counts     Counts
	Identities []Identity // display order: the primary account first (spec §4.2)
}

// Sync is the primary account's score feed: the player's headline sync state.
func (p PlayerSummary) Sync() model.SyncFeed {
	if len(p.Identities) == 0 {
		return model.SyncFeed{}
	}
	return p.Identities[0].Feed(model.KindScore)
}

// Error is the first account or feed error, for compact views.
func (p PlayerSummary) Error() string {
	for _, id := range p.Identities {
		if id.LastError != "" {
			return id.LastError
		}
		for _, f := range id.Feeds {
			if f.LastError != "" {
				return f.LastError
			}
		}
	}
	return ""
}

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

// platformOrder ranks platforms for display: the legacy platform first.
// Task 6 switches it to the registry's priorities.
func (s *Service) platformOrder(name string) int {
	if name == model.PlatformScoreSaber {
		return 0
	}
	return 1
}

// identitiesBy loads accounts with their feeds, for one player or all.
func (s *Service) identitiesBy(ctx context.Context, playerID string) (map[string][]Identity, error) {
	pp, f := s.q.PlayerPlatform, s.q.SyncFeed
	ido, fdo := pp.WithContext(ctx), f.WithContext(ctx)
	if playerID != "" {
		ido, fdo = ido.Where(pp.PlayerID.Eq(playerID)), fdo.Where(f.PlayerID.Eq(playerID))
	}
	ids, err := ido.Find()
	if err != nil {
		return nil, fmt.Errorf("service: load accounts: %w", err)
	}
	feeds, err := fdo.Order(f.Feed).Find()
	if err != nil {
		return nil, fmt.Errorf("service: load feeds: %w", err)
	}
	feedsOf := map[[2]string][]model.SyncFeed{}
	for _, fd := range feeds {
		k := [2]string{fd.PlayerID, fd.Platform}
		feedsOf[k] = append(feedsOf[k], *fd)
	}
	out := map[string][]Identity{}
	for _, id := range ids {
		out[id.PlayerID] = append(out[id.PlayerID], Identity{PlayerPlatform: *id, Feeds: feedsOf[[2]string{id.PlayerID, id.Platform}]})
	}
	for pid := range out {
		slices.SortFunc(out[pid], func(a, b Identity) int {
			return cmp.Or(cmp.Compare(s.platformOrder(a.Platform), s.platformOrder(b.Platform)), cmp.Compare(a.Platform, b.Platform))
		})
	}
	return out, nil
}

// GetPlayerSummary loads one player with counts and accounts.
func (s *Service) GetPlayerSummary(ctx context.Context, id string) (PlayerSummary, error) {
	p, err := s.GetPlayer(ctx, id)
	if err != nil {
		return PlayerSummary{}, err
	}
	counts, err := s.countsBy(ctx, id)
	if err != nil {
		return PlayerSummary{}, err
	}
	ids, err := s.identitiesBy(ctx, id)
	if err != nil {
		return PlayerSummary{}, err
	}
	return PlayerSummary{Player: *p, Counts: counts[id], Identities: ids[id]}, nil
}

func (s *Service) ResolvePlayer(ctx context.Context, input string) (scoresaber.Player, error) {
	id, err := ParsePlayerRef(input)
	if err != nil {
		return scoresaber.Player{}, err
	}
	p, err := s.ss.Player(ctx, id)
	if errors.Is(err, scoresaber.ErrNotFound) {
		return scoresaber.Player{}, fmt.Errorf("%w: no ScoreSaber player %s", ErrNotFound, id)
	}
	return p, err
}

func (s *Service) AddPlayer(ctx context.Context, input string) (*model.Player, error) {
	sp, err := s.ResolvePlayer(ctx, input)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	p := &model.Player{
		ID: sp.ID, Name: sp.Name, AvatarURL: sp.Avatar, Country: sp.Country,
		Enabled: true, AddedAt: now, BackfillState: model.BackfillPending, BackfillPage: 1,
	}
	err = s.q.Transaction(func(tx *query.Query) error {
		if err := tx.Player.WithContext(ctx).Create(p); err != nil {
			return err
		}
		if err := tx.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{
			PlayerID: p.ID, Platform: model.PlatformScoreSaber, ExternalID: sp.ID, Enabled: true, LinkedAt: now,
		}); err != nil {
			return err
		}
		return tx.SyncFeed.WithContext(ctx).Create(newFeed(p.ID, model.PlatformScoreSaber, model.KindScore, now, model.AccessNA))
	})
	if err != nil {
		if db.IsDuplicate(err) {
			return nil, ErrPlayerExists
		}
		return nil, fmt.Errorf("service: add player: %w", err)
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: Ptr(p.ID), Message: "player added: " + p.Name})
	s.Wake()
	return p, nil
}

// Identities lists a player's linked platform accounts.
func (s *Service) Identities(ctx context.Context, playerID string) ([]*model.PlayerPlatform, error) {
	pp := s.q.PlayerPlatform
	out, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID)).Order(pp.Platform).Find()
	if err != nil {
		return nil, fmt.Errorf("service: identities: %w", err)
	}
	return out, nil
}

// Feeds lists a player's sync feeds.
func (s *Service) Feeds(ctx context.Context, playerID string) ([]*model.SyncFeed, error) {
	f := s.q.SyncFeed
	out, err := f.WithContext(ctx).Where(f.PlayerID.Eq(playerID)).Order(f.Platform, f.Feed).Find()
	if err != nil {
		return nil, fmt.Errorf("service: feeds: %w", err)
	}
	return out, nil
}

func (s *Service) GetPlayer(ctx context.Context, id string) (*model.Player, error) {
	p, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).First()
	if err != nil {
		return nil, notFound(err, "player "+id)
	}
	return p, nil
}

func (s *Service) ListPlayers(ctx context.Context, includeDisabled bool) ([]PlayerSummary, error) {
	do := s.q.Player.WithContext(ctx).Order(s.q.Player.Name)
	if !includeDisabled {
		do = do.Where(s.q.Player.Enabled.Is(true))
	}
	players, err := do.Find()
	if err != nil {
		return nil, fmt.Errorf("service: list players: %w", err)
	}
	counts, err := s.countsBy(ctx, "")
	if err != nil {
		return nil, err
	}
	ids, err := s.identitiesBy(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]PlayerSummary, 0, len(players))
	for _, p := range players {
		out = append(out, PlayerSummary{Player: *p, Counts: counts[p.ID], Identities: ids[p.ID]})
	}
	return out, nil
}

func (s *Service) PlayerCounts(ctx context.Context, id string) (Counts, error) {
	counts, err := s.countsBy(ctx, id)
	if err != nil {
		return Counts{}, err
	}
	return counts[id], nil
}

func (s *Service) countsBy(ctx context.Context, playerID string) (map[string]Counts, error) {
	type row struct {
		PlayerID    string
		ReplayState string
		N           int64
	}
	var rows []row
	tx := s.db.WithContext(ctx).Model(&model.Score{}).
		Select("player_id, replay_state, COUNT(*) AS n").Group("player_id, replay_state")
	if playerID != "" {
		tx = tx.Where("player_id = ?", playerID)
	}
	if err := tx.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("service: count scores: %w", err)
	}
	out := map[string]Counts{}
	for _, r := range rows {
		c := out[r.PlayerID]
		c.Scores += r.N
		switch r.ReplayState {
		case model.ReplayArchived:
			c.Archived += r.N
		case model.ReplayPending:
			c.Pending += r.N
		case model.ReplayFailed:
			c.Failed += r.N
		case model.ReplayGone:
			c.Gone += r.N
		}
		out[r.PlayerID] = c
	}
	return out, nil
}

func (s *Service) SetPlayerEnabled(ctx context.Context, id string, enabled bool) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Update(s.q.Player.Enabled, enabled)
	if err != nil {
		return fmt.Errorf("service: set enabled: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	if enabled {
		pp := s.q.PlayerPlatform
		if _, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(id)).Updates(map[string]any{"enabled": true, "last_error": ""}); err != nil {
			return fmt.Errorf("service: enable accounts: %w", err)
		}
		s.Wake()
	}
	return nil
}

func (s *Service) DeletePlayer(ctx context.Context, id string, deleteFiles bool) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Delete()
	if err != nil {
		return fmt.Errorf("service: delete player: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	if deleteFiles {
		if err := s.store.RemovePlayer(id); err != nil {
			return err
		}
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: Ptr(id), Message: fmt.Sprintf("player deleted (files deleted: %v)", deleteFiles)})
	return nil
}
