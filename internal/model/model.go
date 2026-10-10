// Package model holds the GORM models. They are the input of gorm gen
// (internal/db/gen) — run `make generate` after changing them.
package model

import "time"

// Player backfill states.
const (
	BackfillPending = "pending"
	BackfillRunning = "running"
	BackfillDone    = "done"
)

// Score replay states (spec §5).
const (
	ReplayNone     = "none"     // ScoreSaber has no replay for this score
	ReplayPending  = "pending"  // waiting to be downloaded
	ReplayArchived = "archived" // stored on disk
	ReplayGone     = "gone"     // ScoreSaber returned 404 before we got it
	ReplayFailed   = "failed"   // gave up after MaxReplayAttempts
)

// Sync event levels and kinds.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"

	KindPoll      = "poll"
	KindScores    = "scores"
	KindReplay    = "replay"
	KindBackfill  = "backfill"
	KindRateLimit = "ratelimit"
	KindWorker    = "worker"
)

// PlatformScoreSaber is the legacy platform's registry name (spec §4.1). It is
// the only platform name code outside its own package needs (the migration).
const PlatformScoreSaber = "scoresaber"

// Row kinds and end types (spec §4.4). A feed's key in sync_feeds is the row
// kind it produces.
const (
	KindScore   = "score"   // a leaderboard submission
	KindAttempt = "attempt" // any other recorded run

	EndClear    = "clear"
	EndFail     = "fail"
	EndRestart  = "restart"
	EndQuit     = "quit"
	EndPractice = "practice"
	EndUnknown  = "unknown"
)

// Feed access states (spec §4.3).
const (
	AccessNA      = "n/a"     // the feed needs no access probe
	AccessUnknown = "unknown" // not probed yet
	AccessPublic  = "public"
	AccessPrivate = "private"
)

type Player struct {
	ID        string    `gorm:"primaryKey"` // opaque (spec §4.2); never a platform account ID
	Name      string    `gorm:"not null"`
	AvatarURL string    `gorm:"not null"`
	Country   string    `gorm:"not null"`
	Enabled   bool      `gorm:"not null"`
	AddedAt   time.Time `gorm:"not null"`
}

// PlayerPlatform is one linked platform account of a player (spec §4.3).
type PlayerPlatform struct {
	PlayerID   string    `gorm:"primaryKey"`
	Player     *Player   `gorm:"constraint:OnDelete:CASCADE"`
	Platform   string    `gorm:"primaryKey"`
	ExternalID string    `gorm:"not null"` // unique with Platform: index created by db.Migrate
	Enabled    bool      `gorm:"not null"`
	LinkedAt   time.Time `gorm:"not null"`
	LastError  string    `gorm:"not null"`
}

// SyncFeed is the sync cursor of one feed of one platform account (spec
// §4.3). Its table is created by db.Migrate with raw DDL because of the
// composite foreign key to player_platforms: declare no relation here.
type SyncFeed struct {
	PlayerID           string    `gorm:"primaryKey"`
	Platform           string    `gorm:"primaryKey"`
	Feed               string    `gorm:"primaryKey"` // the row kind it produces: KindScore | KindAttempt
	Enabled            bool      `gorm:"not null"`
	DownloadEnabled    bool      `gorm:"not null;default:true"` // false pauses this feed's replay downloads
	StartedAt          time.Time `gorm:"not null"`              // plays set at or after it are "new"
	Access             string    `gorm:"not null"`              // Access*
	AccessCheckedAt    *time.Time
	RemoteTotal        int64  `gorm:"not null"`
	BackfillState      string `gorm:"not null"`
	BackfillPage       int    `gorm:"not null"` // next page to fetch, 1-based
	BackfillTotalPages int    `gorm:"not null"`
	BackfillRetryAt    *time.Time
	LastPolledAt       *time.Time
	LastError          string `gorm:"not null"`
}

// PlayerAlias maps a merged-away player ID to the surviving player (spec §4.2).
type PlayerAlias struct {
	OldID    string  `gorm:"primaryKey"`
	PlayerID string  `gorm:"not null;index"`
	Player   *Player `gorm:"constraint:OnDelete:CASCADE"`
}

type Leaderboard struct {
	ID            int64   `gorm:"primaryKey;autoIncrement:false"`
	SongHash      string  `gorm:"not null;index"`
	SongName      string  `gorm:"not null;index"`
	SongSubName   string  `gorm:"not null"`
	SongAuthor    string  `gorm:"not null"`
	Mapper        string  `gorm:"not null"`
	Difficulty    int     `gorm:"not null"`
	DifficultyRaw string  `gorm:"not null"`
	GameMode      string  `gorm:"not null"`
	CoverURL      string  `gorm:"not null"`
	Status        string  `gorm:"not null"` // RANKED, QUALIFIED, LOVED, UNRANKED
	Stars         float64 `gorm:"not null"`
	MaxScore      int64   `gorm:"not null"`

	Platform   string `gorm:"not null;default:''"` // unique with ExternalID: index created by db.Migrate
	ExternalID string `gorm:"not null;default:''"` // the platform's leaderboard ID
	MapKey     string `gorm:"not null;default:''"` // platform.MapKey; index created by db.Migrate
}

type Score struct {
	ID              int64        `gorm:"primaryKey;autoIncrement:false"`
	PlayerID        string       `gorm:"not null;index:idx_scores_player_set,priority:1"`
	Player          *Player      `gorm:"constraint:OnDelete:CASCADE"`
	LeaderboardID   int64        `gorm:"not null;index"`
	Leaderboard     *Leaderboard `gorm:"constraint:OnDelete:RESTRICT"`
	Rank            int          `gorm:"not null"`
	ModifiedScore   int64        `gorm:"not null"`
	UnmodifiedScore int64        `gorm:"not null"`
	Accuracy        float64      `gorm:"not null"` // 0..1
	PP              float64      `gorm:"not null"`
	Mods            string       `gorm:"not null"` // comma separated
	FullCombo       bool         `gorm:"not null"`
	MissedNotes     int          `gorm:"not null"`
	BadCuts         int          `gorm:"not null"`
	MaxCombo        int          `gorm:"not null"`
	HMD             string       `gorm:"not null"`
	PersonalBest    bool         `gorm:"not null"`
	SetAt           time.Time    `gorm:"not null;index:idx_scores_player_set,priority:2,sort:desc;index:idx_scores_state_set,priority:2,sort:desc"`
	HasReplay       bool         `gorm:"not null"`
	ReplayState     string       `gorm:"not null;index:idx_scores_state_set,priority:1"`
	ReplaySize      int64        `gorm:"not null"`
	ReplaySHA256    string       `gorm:"not null"`
	ArchivedAt      *time.Time
	Attempts        int `gorm:"not null"`
	NextAttemptAt   *time.Time
	LastError       string `gorm:"not null"`

	Platform   string `gorm:"not null;default:''"` // unique with Kind+ExternalID: index created by db.Migrate
	Kind       string `gorm:"not null;default:''"` // KindScore | KindAttempt
	EndType    string `gorm:"not null;default:''"` // End*
	EndTime    *float64
	ExternalID string `gorm:"not null;default:''"` // the platform's score/attempt ID
	ReplayURL  *string
}

type User struct {
	ID           uint      `gorm:"primaryKey"`
	Username     string    `gorm:"not null;uniqueIndex"`
	PasswordHash string    `gorm:"not null"`
	CreatedAt    time.Time `gorm:"not null"`
}

type Session struct {
	TokenHash string    `gorm:"primaryKey"` // hex sha256 of the cookie token
	UserID    uint      `gorm:"not null;index"`
	User      *User     `gorm:"constraint:OnDelete:CASCADE"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"not null"`
}

type Setting struct {
	Key   string `gorm:"primaryKey"`
	Value string `gorm:"not null"`
}

type SyncEvent struct {
	ID       int64     `gorm:"primaryKey"`
	At       time.Time `gorm:"not null;index"`
	Level    string    `gorm:"not null;index"`
	Kind     string    `gorm:"not null;index"`
	PlayerID *string   `gorm:"index"`
	ScoreID  *int64
	Platform *string
	Feed     *string
	Message  string `gorm:"not null"`
}

// All returns every model, in migration order. db.Migrate creates sync_feeds
// by hand before AutoMigrate sees SyncFeed.
func All() []any {
	return []any{
		&Player{}, &PlayerPlatform{}, &SyncFeed{}, &PlayerAlias{}, &Leaderboard{}, &Score{},
		&User{}, &Session{}, &Setting{}, &SyncEvent{},
	}
}
