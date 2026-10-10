// Package platform describes the leaderboard platforms SSArchiver archives
// from (spec §4.1): the neutral types every platform adapter produces, the
// registry, and helpers shared by all platforms. It knows no concrete
// platform; those live in their own packages and are registered at startup.
package platform

import (
	"context"
	"errors"
	"io"
	"time"
)

// Errors adapters wrap so callers can classify failures without knowing the platform.
var (
	ErrNotFound     = errors.New("not found")
	ErrRateLimited  = errors.New("rate limited")
	ErrUnauthorized = errors.New("unauthorized")
	ErrInvalidRef   = errors.New("invalid player reference")
)

// Profile is a player's display identity on one platform.
type Profile struct {
	ExternalID string
	Name       string
	AvatarURL  string
	Country    string
}

// LeaderboardData is one map difficulty as a platform reports it.
type LeaderboardData struct {
	ExternalID    string
	SongHash      string
	SongName      string
	SongSubName   string
	SongAuthor    string
	Mapper        string
	Difficulty    int // 1..9, same scale on every platform
	DifficultyRaw string
	GameMode      string
	CoverURL      string
	Status        string // RANKED, QUALIFIED, LOVED, UNRANKED
	Stars         float64
	MaxScore      int64
}

// Play is one normalized score or attempt (spec §5.2).
type Play struct {
	Leaderboard     LeaderboardData
	Kind            string // model.KindScore | model.KindAttempt
	EndType         string // model.End*
	ExternalID      string
	EndTime         *float64 // seconds into the song when the run ended (attempts)
	Rank            int
	ModifiedScore   int64
	UnmodifiedScore int64
	Accuracy        float64 // 0..1
	PP              float64
	Mods            string // comma separated
	FullCombo       bool
	MissedNotes     int
	BadCuts         int
	MaxCombo        int
	HMD             string
	PersonalBest    bool
	SetAt           time.Time
	HasReplay       bool
	ReplayURL       string   // empty when the platform downloads replays by score ID
	Profile         *Profile // the player's profile when the payload carries one
}

// PlayPage is one page of a feed, newest first.
type PlayPage struct {
	Plays      []Play
	TotalPages int
	Refused    int // plays whose replay URL is not on the platform's allowlist; kept without a replay
	Skipped    int // plays the adapter dropped on purpose (another feed stores them); the page was not empty
}

// ReplayRef identifies the replay of a stored row.
type ReplayRef struct {
	Kind       string
	ExternalID string
	URL        string
}

// Hint tells the admin what a player must do to grant access to a feed (spec §6.3).
type Hint struct {
	Title    string
	Intro    string   // one sentence under the title
	Steps    []string // short imperative steps for the player
	LinkText string
	LinkURL  string
	Note     string // closing remark: re-checks, what is lost while waiting
}

// FeedSpec declares one feed of a platform. Its Kind is also its key in sync_feeds.
type FeedSpec struct {
	Kind        string // model.KindScore | model.KindAttempt
	Optional    bool   // false: created with the account; true: admin opt-in
	NeedsAccess bool   // access must be probed before use (optional feeds only)
	AccessHint  *Hint  // shown while access is private
}

// Platform is one registered leaderboard platform.
type Platform struct {
	Name        string // stored in DB columns, e.g. "scoresaber"
	Slug        string // URL prefix, e.g. "ss"
	DisplayName string
	Priority    int  // lower wins for the player's display identity
	Legacy      bool // ScoreSaber only: bare IDs, legacy routes and storage layout
	PBOnly      bool // the score feed lists current personal bests only: older rows lose personal_best (spec §4.6)
	BSOR        bool // replay files are the open-replay (BSOR) format: the BeatLeader viewer can load them from a .bsor link
	ReplayExt   string
	ImageHosts  []string
	ProfileURL  func(externalID string) string
	ParseURL    func(input string) (externalID string, ok bool)
	ValidID     func(input string) bool
	Feeds       []FeedSpec
	Adapter     Adapter
}

// Feed returns the platform's feed of the given kind.
func (p Platform) Feed(kind string) (FeedSpec, bool) {
	for _, f := range p.Feeds {
		if f.Kind == kind {
			return f, true
		}
	}
	return FeedSpec{}, false
}

// RequiredFeeds are the feeds created with every account of this platform.
func (p Platform) RequiredFeeds() []FeedSpec {
	var out []FeedSpec
	for _, f := range p.Feeds {
		if !f.Optional {
			out = append(out, f)
		}
	}
	return out
}

// Adapter is everything the service and worker need from one platform.
// Implementations wrap ErrNotFound, ErrRateLimited and ErrUnauthorized.
type Adapter interface {
	// Resolve fetches a player's profile; ErrNotFound when the player does not exist.
	Resolve(ctx context.Context, externalID string) (Profile, error)
	// FeedPage returns one page (1-based, newest first) of a feed.
	FeedPage(ctx context.Context, kind, externalID string, page int) (PlayPage, error)
	// ProbeAccess returns model.AccessPublic or model.AccessPrivate for feeds
	// with NeedsAccess, plus the remote item total; model.AccessNA otherwise.
	ProbeAccess(ctx context.Context, kind, externalID string) (access string, total int64, err error)
	// Replay streams one replay; ErrNotFound when it is gone.
	Replay(ctx context.Context, ref ReplayRef) (io.ReadCloser, error)
	// Limiters lists the platform's client-side rate limiters.
	Limiters() []Limiter
	// FeedLimiter and ReplayLimiter name the limiter a listing or a replay
	// download of the given kind consumes ("" = not rate limited).
	FeedLimiter(kind string) string
	ReplayLimiter(kind string) string
}

// Limiter is a named client-side rate limiter.
type Limiter interface {
	Name() string
	// Ready reports whether a request may be sent at now, and if not, from when.
	Ready(now time.Time) (bool, time.Time)
	Snapshot() LimiterSnapshot
}

// WindowSnapshot is one rate-limit window's usage.
type WindowSnapshot struct {
	Name            string
	Limit           int
	Used            int
	Period          time.Duration
	ServerRemaining int // -1 when unknown
	ServerResetAt   time.Time
}

// LimiterSnapshot is a limiter's state for display.
type LimiterSnapshot struct {
	Windows      []WindowSnapshot
	BlockedUntil time.Time // zero when not blocked by server feedback
	Waiting      bool
	WaitUntil    time.Time
}
