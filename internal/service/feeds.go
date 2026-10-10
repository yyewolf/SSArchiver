package service

import (
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// newFeed is a fresh feed cursor: pending backfill from page 1, never polled.
func newFeed(playerID, platformName, kind string, now time.Time, access string) *model.SyncFeed {
	return &model.SyncFeed{
		PlayerID: playerID, Platform: platformName, Feed: kind, Enabled: true, StartedAt: now, Access: access,
		BackfillState: model.BackfillPending, BackfillPage: 1,
	}
}
