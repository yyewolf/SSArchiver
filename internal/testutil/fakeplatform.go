package testutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// FakePlatform is a scripted non-legacy platform for genericity tests (spec
// §9): a required score feed and an optional attempt feed that needs access,
// string IDs, replays fetched by URL. NewFakePlatform is "testplat" (slug
// "tp"); NewFakePlatformAs makes more of them.
type FakePlatform struct {
	mu          sync.Mutex
	Name, Slug  string
	DisplayName string
	urlRe       *regexp.Regexp
	PerPage     int
	Profiles    map[string]platform.Profile
	plays       map[string][]platform.Play // kind + "/" + account, newest first
	Replays     map[string][]byte          // replay URL → bytes
	Calls       []string                   // "kind:account:page"
	Limiter     *FakeLimiter
	PBOnly      bool // registry PBOnly; set before calling Platform()
	Refused     int  // reported as PlayPage.Refused on every page
}

func NewFakePlatform() *FakePlatform { return NewFakePlatformAs("testplat", "tp", "TestPlat") }

// NewFakePlatformAs is a fake platform with its own name, slug ([a-z]{2,8})
// and display name; its profile URLs are https://{slug}.example/u/{id}.
func NewFakePlatformAs(name, slug, displayName string) *FakePlatform {
	return &FakePlatform{
		Name: name, Slug: slug, DisplayName: displayName,
		urlRe:   regexp.MustCompile(`^https://` + slug + `\.example/u/([a-z0-9]{1,16})$`),
		PerPage: 2,
		Profiles: map[string]platform.Profile{
			"abc": {ExternalID: "abc", Name: "Tess", Country: "SE", AvatarURL: "https://img." + slug + ".example/abc.png"},
			"def": {ExternalID: "def", Name: "Dee", Country: "NO", AvatarURL: "https://img." + slug + ".example/def.png"},
		},
		plays:   map[string][]platform.Play{},
		Replays: map[string][]byte{},
		Limiter: &FakeLimiter{name: name},
	}
}

var tpIDRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Platform is the registry entry.
func (f *FakePlatform) Platform() platform.Platform {
	return platform.Platform{
		Name: f.Name, Slug: f.Slug, DisplayName: f.DisplayName, Priority: 50, ReplayExt: ".tpr", PBOnly: f.PBOnly,
		ImageHosts: []string{"https://img." + f.Slug + ".example"},
		ProfileURL: func(id string) string { return "https://" + f.Slug + ".example/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := f.urlRe.FindStringSubmatch(in)
			if m == nil {
				return "", false
			}
			return m[1], true
		},
		ValidID: tpIDRe.MatchString,
		Feeds: []platform.FeedSpec{
			{Kind: model.KindScore},
			{Kind: model.KindAttempt, Optional: true, NeedsAccess: true, AccessHint: &platform.Hint{
				Title: "History is private", Steps: []string{"Open settings", "Make history public"},
				LinkText: "Open settings", LinkURL: "https://" + f.Slug + ".example/settings",
			}},
		},
		Adapter: f,
	}
}

// SetPlays scripts a feed listing (newest first).
func (f *FakePlatform) SetPlays(kind, account string, plays ...platform.Play) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plays[kind+"/"+account] = plays
	for _, p := range plays {
		if p.HasReplay {
			f.Replays[p.ReplayURL] = []byte("tp-replay-" + p.ExternalID)
		}
	}
}

func (f *FakePlatform) Resolve(_ context.Context, id string) (platform.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.Profiles[id]
	if !ok {
		return platform.Profile{}, fmt.Errorf("%w: %s player %s", platform.ErrNotFound, f.Name, id)
	}
	return p, nil
}

func (f *FakePlatform) FeedPage(_ context.Context, kind, account string, page int) (platform.PlayPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, fmt.Sprintf("%s:%s:%d", kind, account, page))
	all := f.plays[kind+"/"+account]
	total := (len(all) + f.PerPage - 1) / f.PerPage
	var out []platform.Play
	if start := (page - 1) * f.PerPage; start < len(all) {
		out = all[start:min(start+f.PerPage, len(all))]
	}
	return platform.PlayPage{Plays: out, TotalPages: total, Refused: f.Refused}, nil
}

func (f *FakePlatform) ProbeAccess(context.Context, string, string) (string, int64, error) {
	return model.AccessPublic, 0, nil
}

func (f *FakePlatform) Replay(_ context.Context, ref platform.ReplayRef) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.Replays[ref.URL]
	if !ok {
		return nil, fmt.Errorf("%w: %s", platform.ErrNotFound, ref.URL)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *FakePlatform) Limiters() []platform.Limiter { return []platform.Limiter{f.Limiter} }
func (f *FakePlatform) FeedLimiter(string) string    { return f.Limiter.Name() }
func (f *FakePlatform) ReplayLimiter(string) string  { return f.Limiter.Name() }

// FakeLimiter is ready unless blocked.
type FakeLimiter struct {
	mu    sync.Mutex
	name  string
	until time.Time
}

func (l *FakeLimiter) Name() string { return l.name }

func (l *FakeLimiter) Block(until time.Time) {
	l.mu.Lock()
	l.until = until
	l.mu.Unlock()
}

func (l *FakeLimiter) Ready(now time.Time) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.until.After(now) {
		return false, l.until
	}
	return true, time.Time{}
}

func (l *FakeLimiter) Snapshot() platform.LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return platform.LimiterSnapshot{BlockedUntil: l.until}
}

// FakePlay builds a testplat play on leaderboard lbExternalID.
func FakePlay(kind, externalID, lbExternalID string, setAt time.Time, replay bool) platform.Play {
	p := platform.Play{
		Leaderboard: platform.LeaderboardData{
			ExternalID: lbExternalID, SongHash: "HASH-" + lbExternalID, SongName: "Song " + lbExternalID,
			Difficulty: 7, DifficultyRaw: "Expert", GameMode: "Standard", Status: "UNRANKED", MaxScore: 1000,
		},
		Kind: kind, EndType: model.EndClear, ExternalID: externalID, ModifiedScore: 900, Accuracy: 0.9,
		PersonalBest: kind == model.KindScore, SetAt: setAt, HasReplay: replay,
	}
	if replay {
		p.ReplayURL = "https://tp.example/replays/" + externalID + ".tpr"
	}
	return p
}
