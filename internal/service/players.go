package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/db"
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

type PlayerSummary struct {
	model.Player
	Counts Counts
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
	p := &model.Player{
		ID: sp.ID, Name: sp.Name, AvatarURL: sp.Avatar, Country: sp.Country,
		Enabled: true, AddedAt: s.Now(), BackfillState: model.BackfillPending, BackfillPage: 1,
	}
	if err := s.q.Player.WithContext(ctx).Create(p); err != nil {
		if db.IsDuplicate(err) {
			return nil, ErrPlayerExists
		}
		return nil, fmt.Errorf("service: add player: %w", err)
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: Ptr(p.ID), Message: "player added: " + p.Name})
	s.Wake()
	return p, nil
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
	out := make([]PlayerSummary, 0, len(players))
	for _, p := range players {
		out = append(out, PlayerSummary{Player: *p, Counts: counts[p.ID]})
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

func (s *Service) RequestPoll(ctx context.Context, id string) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Update(s.q.Player.LastPolledAt, nil)
	if err != nil {
		return fmt.Errorf("service: request poll: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	s.Wake()
	return nil
}
