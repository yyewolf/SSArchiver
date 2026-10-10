package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// BackfillRetryDelay is how long a feed waits after a failed backfill page.
const BackfillRetryDelay = 5 * time.Minute

// FeedKey names one feed of one platform account.
type FeedKey struct{ PlayerID, Platform, Kind string }

func (k FeedKey) String() string { return k.PlayerID + "|" + k.Platform + "|" + k.Kind }

// PlatformKind is a (platform, row kind) pair.
type PlatformKind struct{ Platform, Kind string }

// Busy lists the (platform, kind) pairs the worker must skip for now because
// the limiter they would use is not ready (spec §5.1). nil skips nothing.
type Busy map[PlatformKind]bool

func (b Busy) has(platformName, kind string) bool { return b[PlatformKind{platformName, kind}] }

// WorkFeed is a feed the worker can act on, with what it needs to call the platform.
type WorkFeed struct {
	model.SyncFeed
	ExternalID string // the account ID on the platform — never the player ID
	PlayerName string
}

func (f *WorkFeed) Key() FeedKey { return FeedKey{f.PlayerID, f.Platform, f.Feed} }

// newFeed is a fresh feed cursor: pending backfill from page 1, never polled.
func newFeed(playerID, platformName, kind string, now time.Time, access string) *model.SyncFeed {
	return &model.SyncFeed{
		PlayerID: playerID, Platform: platformName, Feed: kind, Enabled: true, StartedAt: now, Access: access,
		BackfillState: model.BackfillPending, BackfillPage: 1,
	}
}

// workFeedSQL selects the enabled, accessible feeds of enabled accounts of
// enabled players (spec §5.1).
const workFeedSQL = "SELECT f.*, pp.external_id AS external_id, p.name AS player_name FROM sync_feeds f " +
	"JOIN player_platforms pp ON pp.player_id = f.player_id AND pp.platform = f.platform " +
	"JOIN players p ON p.id = f.player_id " +
	"WHERE f.enabled AND pp.enabled AND p.enabled AND f.access IN (?, ?)"

func (s *Service) workFeeds(ctx context.Context, clause string, args ...any) ([]*WorkFeed, error) {
	var out []*WorkFeed
	all := append([]any{model.AccessNA, model.AccessPublic}, args...)
	if err := s.db.WithContext(ctx).Raw(workFeedSQL+clause, all...).Scan(&out).Error; err != nil {
		return nil, fmt.Errorf("service: load feeds: %w", err)
	}
	return out, nil
}

// DueFeed returns the feed whose poll is most overdue — score feeds before
// other kinds — skipping busy ones; nil when nothing is due.
func (s *Service) DueFeed(ctx context.Context, interval time.Duration, busy Busy) (*WorkFeed, error) {
	feeds, err := s.workFeeds(ctx,
		" AND (f.last_polled_at IS NULL OR f.last_polled_at <= ?) ORDER BY f.last_polled_at, f.player_id, f.platform, f.feed",
		s.Now().Add(-interval))
	if err != nil {
		return nil, err
	}
	var other *WorkFeed
	for _, f := range feeds {
		if busy.has(f.Platform, f.Feed) {
			continue
		}
		if f.Feed == model.KindScore {
			return f, nil
		}
		if other == nil {
			other = f
		}
	}
	return other, nil
}

// NextPollAt is when the next non-busy feed becomes due (ok=false: none).
func (s *Service) NextPollAt(ctx context.Context, interval time.Duration, busy Busy) (time.Time, bool, error) {
	feeds, err := s.workFeeds(ctx, " ORDER BY f.last_polled_at") // SQLite sorts NULL first
	if err != nil {
		return time.Time{}, false, err
	}
	for _, f := range feeds {
		if busy.has(f.Platform, f.Feed) {
			continue
		}
		if f.LastPolledAt == nil {
			return s.Now(), true, nil
		}
		return f.LastPolledAt.Add(interval), true, nil
	}
	return time.Time{}, false, nil
}

// NextBackfillFeed picks, round robin after last (a FeedKey string), an
// already-polled feed whose backfill is not done and not deferred. scores
// selects score feeds (tier 4); otherwise the other kinds (tier 6).
func (s *Service) NextBackfillFeed(ctx context.Context, last string, scores bool, busy Busy) (*WorkFeed, error) {
	op := "="
	if !scores {
		op = "<>"
	}
	feeds, err := s.workFeeds(ctx,
		" AND f.feed "+op+" ? AND f.backfill_state <> ? AND f.last_polled_at IS NOT NULL"+
			" AND (f.backfill_retry_at IS NULL OR f.backfill_retry_at <= ?)",
		model.KindScore, model.BackfillDone, s.Now())
	if err != nil {
		return nil, err
	}
	byKey := map[string]*WorkFeed{}
	var keys []string
	for _, f := range feeds {
		if busy.has(f.Platform, f.Feed) {
			continue
		}
		k := f.Key().String()
		byKey[k] = f
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	slices.Sort(keys)
	return byKey[pickAfter(keys, last)], nil
}

func (s *Service) updateFeed(ctx context.Context, k FeedKey, upd map[string]any) error {
	f := s.q.SyncFeed
	info, err := f.WithContext(ctx).Where(f.PlayerID.Eq(k.PlayerID), f.Platform.Eq(k.Platform), f.Feed.Eq(k.Kind)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: update feed %s: %w", k, err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: feed %s", ErrNotFound, k)
	}
	return nil
}

// MarkFeedPolled records a successful poll and clears the feed's error.
func (s *Service) MarkFeedPolled(ctx context.Context, k FeedKey) error {
	return s.updateFeed(ctx, k, map[string]any{"last_polled_at": s.Now(), "last_error": ""})
}

func (s *Service) MarkFeedError(ctx context.Context, k FeedKey, msg string) error {
	return s.updateFeed(ctx, k, map[string]any{"last_error": msg})
}

func (s *Service) SetFeedBackfill(ctx context.Context, k FeedKey, state string, nextPage, totalPages int) error {
	return s.updateFeed(ctx, k, map[string]any{
		"backfill_state": state, "backfill_page": nextPage, "backfill_total_pages": totalPages, "backfill_retry_at": nil,
	})
}

func (s *Service) DeferFeedBackfill(ctx context.Context, k FeedKey, until time.Time, msg string) error {
	return s.updateFeed(ctx, k, map[string]any{"backfill_retry_at": until, "last_error": msg})
}

// MarkIdentityError records an account-level error; disable stops every feed
// of that account (the platform reports the player gone). The player and its
// other accounts keep running.
func (s *Service) MarkIdentityError(ctx context.Context, playerID, platformName, msg string, disable bool) error {
	pp := s.q.PlayerPlatform
	upd := map[string]any{"last_error": msg}
	if disable {
		upd["enabled"] = false
	}
	info, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: account error: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: %s account of player %s", ErrNotFound, platformName, playerID)
	}
	return nil
}

// RequestPoll makes every feed of a player due now.
func (s *Service) RequestPoll(ctx context.Context, id string) error {
	if _, err := s.GetPlayer(ctx, id); err != nil {
		return err
	}
	f := s.q.SyncFeed
	if _, err := f.WithContext(ctx).Where(f.PlayerID.Eq(id)).Update(f.LastPolledAt, nil); err != nil {
		return fmt.Errorf("service: request poll: %w", err)
	}
	s.Wake()
	return nil
}
