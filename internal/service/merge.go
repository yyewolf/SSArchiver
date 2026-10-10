package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
)

// ResolvePlayerID follows merge aliases (spec §4.2): a live player resolves to
// itself, a merged-away ID to its survivor (aliased=true).
func (s *Service) ResolvePlayerID(ctx context.Context, id string) (string, bool, error) {
	if _, err := s.GetPlayer(ctx, id); err == nil {
		return id, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", false, err
	}
	a := s.q.PlayerAlias
	al, err := a.WithContext(ctx).Where(a.OldID.Eq(id)).First()
	if err != nil {
		return "", false, notFound(err, "player "+id)
	}
	return al.PlayerID, true, nil
}

// MergePlayers moves every account, feed, score and replay of source onto
// into, and leaves source as an alias of into (spec §6.2). Allowed only when
// the players have no platform in common, so nothing is ever dropped. Files
// are linked first, the database moves in one transaction, and the source
// directory goes last: a crash at any point leaves at worst orphan copies.
func (s *Service) MergePlayers(ctx context.Context, sourceID, intoID string) error {
	if sourceID == intoID {
		return ErrMergeSelf
	}
	src, err := s.GetPlayerSummary(ctx, sourceID)
	if err != nil {
		return err
	}
	dst, err := s.GetPlayerSummary(ctx, intoID)
	if err != nil {
		return err
	}
	for _, a := range src.Identities {
		for _, b := range dst.Identities {
			if a.Platform == b.Platform {
				return fmt.Errorf("%w: %s", ErrMergeConflict, s.displayName(a.Platform))
			}
		}
	}

	// 1. Files: make every archived replay reachable under the target first.
	q := s.q.Score
	archived, err := q.WithContext(ctx).Where(q.PlayerID.Eq(sourceID), q.ReplayState.Eq(model.ReplayArchived)).Find()
	if err != nil {
		return fmt.Errorf("service: merge: %w", err)
	}
	for _, sc := range archived {
		from, err := s.replayLoc(sc)
		if err != nil {
			continue // a platform that is no longer registered: its rows move, its files stay put
		}
		to := from
		to.PlayerID = intoID
		if err := s.store.Link(from, to); err != nil {
			return fmt.Errorf("service: merge: %w", err)
		}
	}

	// 2. Database.
	if err := s.q.Transaction(func(tx *query.Query) error { return s.mergeRows(ctx, tx, src, dst) }); err != nil {
		return fmt.Errorf("service: merge: %w", err)
	}

	// 3. The source directory now only holds duplicates.
	if err := s.store.RemovePlayer(sourceID); err != nil {
		slog.Warn("merge: source replay directory left behind", "player", sourceID, "err", err)
	}
	s.Log(ctx, model.SyncEvent{
		Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(intoID),
		Message: "merged " + src.Name + " into " + dst.Name,
	})
	return nil
}

func (s *Service) mergeRows(ctx context.Context, tx *query.Query, src, dst PlayerSummary) error {
	from, into := src.ID, dst.ID
	// sync_feeds references player_platforms(player_id, platform) without
	// ON UPDATE: lift the feeds out, move the accounts, put the feeds back.
	f := tx.SyncFeed
	feeds, err := f.WithContext(ctx).Where(f.PlayerID.Eq(from)).Find()
	if err != nil {
		return err
	}
	if _, err := f.WithContext(ctx).Where(f.PlayerID.Eq(from)).Delete(); err != nil {
		return err
	}
	pp := tx.PlayerPlatform
	if _, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(from)).Update(pp.PlayerID, into); err != nil {
		return err
	}
	for _, fd := range feeds {
		fd.PlayerID = into
	}
	if len(feeds) > 0 {
		if err := f.WithContext(ctx).Create(feeds...); err != nil {
			return err
		}
	}
	sc := tx.Score
	if _, err := sc.WithContext(ctx).Where(sc.PlayerID.Eq(from)).Update(sc.PlayerID, into); err != nil {
		return err
	}
	al := tx.PlayerAlias
	if _, err := al.WithContext(ctx).Where(al.PlayerID.Eq(from)).Update(al.PlayerID, into); err != nil {
		return err
	}
	if err := al.WithContext(ctx).Create(&model.PlayerAlias{OldID: from, PlayerID: into}); err != nil {
		return err
	}
	ev := tx.SyncEvent
	if _, err := ev.WithContext(ctx).Where(ev.PlayerID.Eq(from)).Update(ev.PlayerID, into); err != nil {
		return err
	}
	if s.bestPriority(src) < s.bestPriority(dst) { // the survivor's primary account now comes from the source
		p := tx.Player
		if _, err := p.WithContext(ctx).Where(p.ID.Eq(into)).Updates(map[string]any{
			"name": src.Name, "avatar_url": src.AvatarURL, "country": src.Country,
		}); err != nil {
			return err
		}
	}
	_, err = tx.Player.WithContext(ctx).Where(tx.Player.ID.Eq(from)).Delete()
	return err
}

// bestPriority is the registry priority of the player's primary account.
func (s *Service) bestPriority(p PlayerSummary) int {
	best := int(^uint(0) >> 1)
	for _, id := range p.Identities {
		best = min(best, s.platformOrder(id.Platform))
	}
	return best
}
