package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// Access re-check timings (spec §5.1).
const (
	AccessRecheck   = 24 * time.Hour  // a private feed is probed again after this
	ProbeRetryDelay = 5 * time.Minute // an unknown feed whose probe failed waits this long
)

var ErrFeedNotOptional = errors.New("only optional feeds can be switched on or off")

// feedSpec finds a feed's declaration in the registry.
func (s *Service) feedSpec(platformName, kind string) (platform.Platform, platform.FeedSpec, error) {
	p, ok := s.reg.Get(platformName)
	if !ok {
		return platform.Platform{}, platform.FeedSpec{}, fmt.Errorf("%w: platform %s", ErrNotFound, platformName)
	}
	f, ok := p.Feed(kind)
	if !ok {
		return p, platform.FeedSpec{}, fmt.Errorf("%w: %s has no %s feed", ErrNotFound, p.DisplayName, kind)
	}
	return p, f, nil
}

func (s *Service) getFeed(ctx context.Context, k FeedKey) (*model.SyncFeed, error) {
	f := s.q.SyncFeed
	row, err := f.WithContext(ctx).Where(f.PlayerID.Eq(k.PlayerID), f.Platform.Eq(k.Platform), f.Feed.Eq(k.Kind)).First()
	if err != nil {
		return nil, notFound(err, "feed "+k.String())
	}
	return row, nil
}

func (s *Service) account(ctx context.Context, playerID, platformName string) (*model.PlayerPlatform, error) {
	pp := s.q.PlayerPlatform
	link, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).First()
	if err != nil {
		return nil, notFound(err, platformName+" account of player "+playerID)
	}
	return link, nil
}

// SetFeedEnabled switches an optional feed on or off (spec §4.3). Its row is
// created the first time it is switched on. Switching on (again) restarts
// its clock — plays from then on are "new" work — and, for a feed that needs
// access, the access check. Switching off keeps the cursor, rows and files.
func (s *Service) SetFeedEnabled(ctx context.Context, k FeedKey, enabled bool) (*model.SyncFeed, error) {
	p, spec, err := s.feedSpec(k.Platform, k.Kind)
	if err != nil {
		return nil, err
	}
	if !spec.Optional {
		return nil, ErrFeedNotOptional
	}
	if _, err := s.account(ctx, k.PlayerID, k.Platform); err != nil {
		return nil, err
	}
	now := s.Now()
	access := model.AccessNA
	if spec.NeedsAccess {
		access = model.AccessUnknown
	}
	cur, err := s.getFeed(ctx, k)
	switch {
	case errors.Is(err, ErrNotFound) && !enabled:
		off := newFeed(k.PlayerID, k.Platform, k.Kind, now, access) // nothing to store: it was never on
		off.Enabled = false
		return off, nil
	case errors.Is(err, ErrNotFound):
		if err := s.q.SyncFeed.WithContext(ctx).Create(newFeed(k.PlayerID, k.Platform, k.Kind, now, access)); err != nil {
			return nil, fmt.Errorf("service: create feed: %w", err)
		}
	case err != nil:
		return nil, err
	case enabled && !cur.Enabled:
		upd := map[string]any{"enabled": true, "started_at": now, "last_error": ""}
		if spec.NeedsAccess {
			upd["access"], upd["access_checked_at"] = model.AccessUnknown, nil
		}
		if err := s.updateFeed(ctx, k, upd); err != nil {
			return nil, err
		}
	case !enabled && cur.Enabled:
		if err := s.updateFeed(ctx, k, map[string]any{"enabled": false}); err != nil {
			return nil, err
		}
	}
	state := "off"
	if enabled {
		state = "on"
		s.Wake()
	}
	s.Log(ctx, model.SyncEvent{
		Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(k.PlayerID), Platform: new(k.Platform), Feed: new(k.Kind),
		Message: fmt.Sprintf("archiving of %s %ss switched %s", p.DisplayName, k.Kind, state),
	})
	return s.getFeed(ctx, k)
}

// CheckFeedAccess probes whether this instance may read a feed and stores
// the result (spec §5.1): access, access_checked_at, and remote_total when
// public. A failed probe is recorded in last_error (except rate limiting,
// which records nothing) and returned along with the stored feed.
func (s *Service) CheckFeedAccess(ctx context.Context, k FeedKey) (*model.SyncFeed, error) {
	p, spec, err := s.feedSpec(k.Platform, k.Kind)
	if err != nil {
		return nil, err
	}
	cur, err := s.getFeed(ctx, k)
	if err != nil {
		return nil, err
	}
	if !spec.NeedsAccess {
		return cur, nil
	}
	link, err := s.account(ctx, k.PlayerID, k.Platform)
	if err != nil {
		return nil, err
	}
	access, total, perr := p.Adapter.ProbeAccess(ctx, k.Kind, link.ExternalID)
	if perr != nil {
		if ctx.Err() == nil && !errors.Is(perr, platform.ErrRateLimited) {
			if err := s.updateFeed(ctx, k, map[string]any{"access_checked_at": s.Now(), "last_error": "access check failed: " + perr.Error()}); err != nil {
				return cur, err
			}
		}
		f, err := s.getFeed(ctx, k)
		if err != nil {
			return cur, err
		}
		return f, fmt.Errorf("service: access check: %w", perr)
	}
	upd := map[string]any{"access": access, "access_checked_at": s.Now(), "last_error": ""}
	if access == model.AccessPublic {
		upd["remote_total"] = total
	}
	if err := s.updateFeed(ctx, k, upd); err != nil {
		return cur, err
	}
	if access != cur.Access {
		ev := model.SyncEvent{Kind: model.KindPoll, PlayerID: new(k.PlayerID), Platform: new(k.Platform), Feed: new(k.Kind)}
		switch access {
		case model.AccessPrivate:
			ev.Level, ev.Message = model.LevelWarn, accessDeniedMessage(p, spec)
			s.Log(ctx, ev)
		case model.AccessPublic:
			ev.Level, ev.Message = model.LevelInfo, fmt.Sprintf("%s %s history is public: archiving it", p.DisplayName, k.Kind)
			s.Log(ctx, ev)
			s.Wake()
		}
	}
	return s.getFeed(ctx, k)
}

// accessDeniedMessage is the warning logged when a feed turns private: its
// hint title when the platform gives one.
func accessDeniedMessage(p platform.Platform, spec platform.FeedSpec) string {
	if spec.AccessHint != nil && spec.AccessHint.Title != "" {
		return spec.AccessHint.Title
	}
	return fmt.Sprintf("%s refused access to the %s feed", p.DisplayName, spec.Kind)
}

// DueProbe returns the feed whose access check is most overdue, skipping
// busy platforms: unknown feeds at once (5 minutes after a failed probe),
// private ones every 24 hours (spec §5.1). nil when none is due.
func (s *Service) DueProbe(ctx context.Context, busy Busy) (*WorkFeed, error) {
	now := s.Now()
	var feeds []*WorkFeed
	err := s.db.WithContext(ctx).Raw(feedSelect+
		" AND ((f.access = ? AND (f.access_checked_at IS NULL OR f.access_checked_at <= ?))"+
		" OR (f.access = ? AND (f.access_checked_at IS NULL OR f.access_checked_at <= ?)))"+
		" ORDER BY f.access_checked_at, f.player_id, f.platform, f.feed", // SQLite sorts NULL first
		model.AccessUnknown, now.Add(-ProbeRetryDelay), model.AccessPrivate, now.Add(-AccessRecheck)).Scan(&feeds).Error
	if err != nil {
		return nil, fmt.Errorf("service: due probe: %w", err)
	}
	for _, f := range feeds {
		if !busy.has(f.Platform, f.Feed) {
			return f, nil
		}
	}
	return nil, nil
}

// MarkFeedPrivate records a feed that lost access mid-run (a 401/403 on a
// listing or a replay, spec §5.1): it stops until a probe says otherwise.
// Its rows, files and cursor are kept.
func (s *Service) MarkFeedPrivate(ctx context.Context, k FeedKey) error {
	return s.updateFeed(ctx, k, map[string]any{"access": model.AccessPrivate, "access_checked_at": s.Now()})
}
