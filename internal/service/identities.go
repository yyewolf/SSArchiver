package service

import (
	"context"
	"fmt"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
)

// LinkIdentity links a platform account to an existing player (spec §6.2).
// The account is resolved live. It is refused when the player already has an
// account on that platform, or when the account is tracked as another player
// (*LinkedElsewhereError). Its required feeds start now, so its older plays
// are backfill work.
func (s *Service) LinkIdentity(ctx context.Context, playerID, ref, platformName string) (*model.PlayerPlatform, error) {
	if platformName == "" {
		return nil, fmt.Errorf("%w: choose the account's platform", ErrInvalidPlayerRef)
	}
	if _, err := s.GetPlayer(ctx, playerID); err != nil {
		return nil, err
	}
	if err := s.checkNoAccountOn(ctx, playerID, platformName); err != nil {
		return nil, err
	}
	plat, prof, err := s.ResolvePlayer(ctx, ref, platformName)
	if err != nil {
		return nil, err
	}
	if err := s.linkedElsewhere(ctx, plat, prof.ExternalID); err != nil {
		return nil, err
	}
	now := s.Now()
	link := &model.PlayerPlatform{PlayerID: playerID, Platform: plat.Name, ExternalID: prof.ExternalID, Enabled: true, LinkedAt: now}
	err = s.q.Transaction(func(tx *query.Query) error {
		if err := tx.PlayerPlatform.WithContext(ctx).Create(link); err != nil {
			return err
		}
		return createRequiredFeeds(ctx, tx, playerID, plat, now)
	})
	if db.IsDuplicate(err) { // lost a race with another link or add
		if lerr := s.linkedElsewhere(ctx, plat, prof.ExternalID); lerr != nil {
			return nil, lerr
		}
		return nil, ErrPlatformAlreadyLinked
	}
	if err != nil {
		return nil, fmt.Errorf("service: link: %w", err)
	}
	s.Log(ctx, model.SyncEvent{
		Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(playerID), Platform: new(plat.Name),
		Message: "linked " + plat.DisplayName + " account " + prof.ExternalID,
	})
	s.Wake()
	return link, nil
}

func (s *Service) checkNoAccountOn(ctx context.Context, playerID, platformName string) error {
	pp := s.q.PlayerPlatform
	n, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).Count()
	if err != nil {
		return fmt.Errorf("service: link: %w", err)
	}
	if n > 0 {
		return ErrPlatformAlreadyLinked
	}
	return nil
}

// UnlinkIdentity removes one account of a player with its feeds and rows
// (spec §6.2), and its replay files when deleteFiles is set. A player keeps
// at least one account.
func (s *Service) UnlinkIdentity(ctx context.Context, playerID, platformName string, deleteFiles bool) error {
	ids, err := s.Identities(ctx, playerID)
	if err != nil {
		return err
	}
	found := false
	for _, id := range ids {
		found = found || id.Platform == platformName
	}
	if !found {
		return fmt.Errorf("%w: %s account of player %s", ErrNotFound, platformName, playerID)
	}
	if len(ids) == 1 {
		return ErrLastIdentity
	}
	q := s.q.Score
	var archived []*model.Score
	if deleteFiles {
		if archived, err = q.WithContext(ctx).Where(q.PlayerID.Eq(playerID), q.Platform.Eq(platformName),
			q.ReplayState.Eq(model.ReplayArchived)).Find(); err != nil {
			return fmt.Errorf("service: unlink: %w", err)
		}
	}
	err = s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.Score.WithContext(ctx).Where(tx.Score.PlayerID.Eq(playerID), tx.Score.Platform.Eq(platformName)).Delete(); err != nil {
			return err
		}
		// Feeds go with the account (composite foreign key, ON DELETE CASCADE).
		pp := tx.PlayerPlatform
		_, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).Delete()
		return err
	})
	if err != nil {
		return fmt.Errorf("service: unlink: %w", err)
	}
	if deleteFiles {
		if err := s.removeFiles(playerID, platformName, archived); err != nil {
			return err
		}
	}
	s.Log(ctx, model.SyncEvent{
		Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(playerID), Platform: new(platformName),
		Message: fmt.Sprintf("unlinked %s account (files deleted: %v)", s.displayName(platformName), deleteFiles),
	})
	return nil
}

// removeFiles deletes one platform's replay files of a player: its directory
// in the per-platform layout, file by file in the legacy one.
func (s *Service) removeFiles(playerID, platformName string, archived []*model.Score) error {
	if p, ok := s.reg.Get(platformName); ok && !p.Legacy {
		return s.store.RemoveDir(playerID, p.Name)
	}
	for _, sc := range archived {
		l, err := s.replayLoc(sc)
		if err != nil {
			continue
		}
		if err := s.store.Remove(l); err != nil {
			return err
		}
	}
	return nil
}

// SetIdentityEnabled pauses or resumes one account; resuming clears its error.
func (s *Service) SetIdentityEnabled(ctx context.Context, playerID, platformName string, enabled bool) error {
	pp := s.q.PlayerPlatform
	upd := map[string]any{"enabled": enabled}
	if enabled {
		upd["last_error"] = ""
	}
	info, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: set account enabled: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: %s account of player %s", ErrNotFound, platformName, playerID)
	}
	if enabled {
		s.Wake()
	}
	return nil
}

// displayName is a platform's display name (its name when unregistered).
func (s *Service) displayName(platformName string) string {
	if p, ok := s.reg.Get(platformName); ok {
		return p.DisplayName
	}
	return platformName
}
