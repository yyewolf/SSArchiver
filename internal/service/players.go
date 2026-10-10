package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

type Counts struct {
	Scores, Archived, Pending, Failed, Gone int64
}

// Replays is the number of score rows the platform offered a replay for.
func (c Counts) Replays() int64 { return c.Archived + c.Pending + c.Failed + c.Gone }

func (c *Counts) add(state string, n int64) {
	c.Scores += n
	switch state {
	case model.ReplayArchived:
		c.Archived += n
	case model.ReplayPending:
		c.Pending += n
	case model.ReplayFailed:
		c.Failed += n
	case model.ReplayGone:
		c.Gone += n
	}
}

// playerCounts are one player's row counts.
type playerCounts struct {
	scores Counts                  // score rows of every platform: the headline counts
	byKind map[PlatformKind]Counts // per platform and row kind
}

func (s *Service) countsBy(ctx context.Context, playerID string) (map[string]playerCounts, error) {
	type row struct {
		PlayerID, Platform, Kind, ReplayState string
		N                                     int64
	}
	var rows []row
	tx := s.db.WithContext(ctx).Model(&model.Score{}).
		Select("player_id, platform, kind, replay_state, COUNT(*) AS n").Group("player_id, platform, kind, replay_state")
	if playerID != "" {
		tx = tx.Where("player_id = ?", playerID)
	}
	if err := tx.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("service: count scores: %w", err)
	}
	out := map[string]playerCounts{}
	for _, r := range rows {
		pc := out[r.PlayerID]
		if pc.byKind == nil {
			pc.byKind = map[PlatformKind]Counts{}
		}
		k := PlatformKind{r.Platform, r.Kind}
		c := pc.byKind[k]
		c.add(r.ReplayState, r.N)
		pc.byKind[k] = c
		if r.Kind == model.KindScore {
			pc.scores.add(r.ReplayState, r.N)
		}
		out[r.PlayerID] = pc
	}
	return out, nil
}

// withCounts fills each account's per-kind counts.
func withCounts(ids []Identity, pc playerCounts) []Identity {
	for i := range ids {
		ids[i].Counts = map[string]Counts{}
		for k, c := range pc.byKind {
			if k.Platform == ids[i].Platform {
				ids[i].Counts[k.Kind] = c
			}
		}
	}
	return ids
}

// Identity is one linked platform account with its feeds and row counts.
type Identity struct {
	model.PlayerPlatform
	Feeds  []model.SyncFeed
	Counts map[string]Counts // by row kind (model.Kind*)
}

// Scores are the counts of the account's score rows.
func (i Identity) Scores() Counts { return i.Counts[model.KindScore] }

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
func (s *Service) UpdatePlayerProfile(ctx context.Context, id string, prof platform.Profile) error {
	upd := map[string]any{}
	if prof.Name != "" {
		upd["name"] = prof.Name
	}
	if prof.AvatarURL != "" {
		upd["avatar_url"] = prof.AvatarURL
	}
	if prof.Country != "" {
		upd["country"] = prof.Country
	}
	if len(upd) == 0 {
		return nil
	}
	return s.updatePlayer(ctx, id, upd)
}

// platformOrder ranks platforms for display: the legacy platform first.
func (s *Service) platformOrder(name string) int {
	if s.reg == nil {
		return 0
	}
	return s.reg.Priority(name)
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
	return PlayerSummary{Player: *p, Counts: counts[id].scores, Identities: withCounts(ids[id], counts[id])}, nil
}

// ResolvePlayer parses an admin's reference (profile URL or ID; platformName
// "" auto-detects) and looks the account up live.
func (s *Service) ResolvePlayer(ctx context.Context, ref, platformName string) (platform.Platform, platform.Profile, error) {
	p, id, err := s.reg.ParseRef(ref, platformName)
	if err != nil {
		// "invalid player reference: paste a profile URL or a player ID"
		return platform.Platform{}, platform.Profile{}, fmt.Errorf("%w%s", ErrInvalidPlayerRef, strings.TrimPrefix(err.Error(), platform.ErrInvalidRef.Error()))
	}
	prof, err := p.Adapter.Resolve(ctx, id)
	if errors.Is(err, platform.ErrNotFound) {
		return p, platform.Profile{}, fmt.Errorf("%w: no %s player %s", ErrNotFound, p.DisplayName, id)
	}
	if err != nil {
		return p, platform.Profile{}, err
	}
	if prof.ExternalID == "" {
		prof.ExternalID = id
	}
	return p, prof, nil
}

// PlayerByIdentity finds the player a platform account is linked to.
func (s *Service) PlayerByIdentity(ctx context.Context, platformName, externalID string) (*model.Player, error) {
	pp := s.q.PlayerPlatform
	link, err := pp.WithContext(ctx).Where(pp.Platform.Eq(platformName), pp.ExternalID.Eq(externalID)).First()
	if err != nil {
		return nil, notFound(err, platformName+" account "+externalID)
	}
	return s.GetPlayer(ctx, link.PlayerID)
}

// freePlayerID draws IDs until one is used by neither a player nor an alias.
func (s *Service) freePlayerID(ctx context.Context) (string, error) {
	p, a := s.q.Player, s.q.PlayerAlias
	for range 10 {
		id := s.nextID()
		np, err := p.WithContext(ctx).Where(p.ID.Eq(id)).Count()
		if err != nil {
			return "", fmt.Errorf("service: player id: %w", err)
		}
		na, err := a.WithContext(ctx).Where(a.OldID.Eq(id)).Count()
		if err != nil {
			return "", fmt.Errorf("service: player id: %w", err)
		}
		if np == 0 && na == 0 {
			return id, nil
		}
	}
	return "", errors.New("service: could not draw a free player ID")
}

// linkedElsewhere returns a *LinkedElsewhereError when the account is
// already linked to a player, nil when it is free.
func (s *Service) linkedElsewhere(ctx context.Context, plat platform.Platform, externalID string) error {
	p, err := s.PlayerByIdentity(ctx, plat.Name, externalID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return &LinkedElsewhereError{Platform: plat.DisplayName, ExternalID: externalID, PlayerID: p.ID, PlayerName: p.Name}
}

// createRequiredFeeds creates the always-on feeds of a new account.
func createRequiredFeeds(ctx context.Context, tx *query.Query, playerID string, plat platform.Platform, now time.Time) error {
	for _, f := range plat.RequiredFeeds() {
		if err := tx.SyncFeed.WithContext(ctx).Create(newFeed(playerID, plat.Name, f.Kind, now, model.AccessNA)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) AddPlayer(ctx context.Context, ref, platformName string) (*model.Player, error) {
	plat, prof, err := s.ResolvePlayer(ctx, ref, platformName)
	if err != nil {
		return nil, err
	}
	if err := s.linkedElsewhere(ctx, plat, prof.ExternalID); err != nil {
		return nil, err
	}
	id, err := s.freePlayerID(ctx)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	p := &model.Player{ID: id, Name: prof.Name, AvatarURL: prof.AvatarURL, Country: prof.Country, Enabled: true, AddedAt: now}
	err = s.q.Transaction(func(tx *query.Query) error {
		if err := tx.Player.WithContext(ctx).Create(p); err != nil {
			return err
		}
		if err := tx.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{
			PlayerID: id, Platform: plat.Name, ExternalID: prof.ExternalID, Enabled: true, LinkedAt: now,
		}); err != nil {
			return err
		}
		return createRequiredFeeds(ctx, tx, id, plat, now)
	})
	if err != nil {
		if db.IsDuplicate(err) {
			return nil, ErrPlayerExists
		}
		return nil, fmt.Errorf("service: add player: %w", err)
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(p.ID), Platform: new(plat.Name), Message: "player added: " + p.Name})
	s.Wake()
	return p, nil
}

// RefreshProfile updates the player's display identity from one platform's
// profile, but only when that platform is the player's primary enabled
// account (spec §4.2).
func (s *Service) RefreshProfile(ctx context.Context, playerID, platformName string, prof platform.Profile) error {
	ids, err := s.identitiesBy(ctx, playerID)
	if err != nil {
		return err
	}
	for _, id := range ids[playerID] {
		if !id.Enabled {
			continue
		}
		if id.Platform != platformName {
			return nil
		}
		return s.UpdatePlayerProfile(ctx, playerID, prof)
	}
	return nil
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
		out = append(out, PlayerSummary{Player: *p, Counts: counts[p.ID].scores, Identities: withCounts(ids[p.ID], counts[p.ID])})
	}
	return out, nil
}

func (s *Service) PlayerCounts(ctx context.Context, id string) (Counts, error) {
	counts, err := s.countsBy(ctx, id)
	if err != nil {
		return Counts{}, err
	}
	return counts[id].scores, nil
}

func (s *Service) SetPlayerEnabled(ctx context.Context, id string, enabled bool) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Update(s.q.Player.Enabled, enabled)
	if err != nil {
		return fmt.Errorf("service: set enabled: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	s.Publish(Update{PlayerID: id, Kind: model.KindWorker})
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
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(id), Message: fmt.Sprintf("player deleted (files deleted: %v)", deleteFiles)})
	return nil
}
