package beatleader

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// Name is BeatLeader's registry name, stored in the database.
const Name = "beatleader"

// API is the part of the client the adapter uses (*Client implements it;
// tests pass fakes).
type API interface {
	Player(ctx context.Context, id string) (Player, error)
	Scores(ctx context.Context, playerID string, page int) (ScorePage, error)
	Replay(ctx context.Context, url string) (io.ReadCloser, error)
	ReplayAllowed(url string) bool
}

var (
	profileURLRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?beatleader\.(?:com|xyz)/u/([A-Za-z0-9_.-]{1,64})/?(?:[?#].*)?$`)
	idRe         = regexp.MustCompile(`^[0-9]{1,32}$`)
)

// NewPlatform is BeatLeader's registry entry (spec §5.3). Nil limiters
// (tests) mean no client-side rate limiting.
func NewPlatform(api API, apiL, cdnL *Limiter) platform.Platform {
	return platform.Platform{
		Name: Name, Slug: "bl", DisplayName: "BeatLeader", Priority: 10, PBOnly: true, ReplayExt: ".bsor",
		ImageHosts: []string{
			"https://cdn.assets.beatleader.xyz", "https://cdn.beatsaver.com", "https://*.cdn.beatsaver.com",
			"https://avatars.akamai.steamstatic.com", "https://avatars.steamstatic.com",
		},
		ProfileURL: func(id string) string { return "https://beatleader.com/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := profileURLRe.FindStringSubmatch(strings.TrimSpace(in))
			if m == nil {
				return "", false
			}
			return m[1], true
		},
		ValidID: idRe.MatchString,
		Feeds:   []platform.FeedSpec{{Kind: model.KindScore}},
		Adapter: adapter{api: api, apiL: apiL, cdnL: cdnL},
	}
}

type adapter struct {
	api        API
	apiL, cdnL *Limiter
}

// Resolve looks a player up; the profile carries BeatLeader's canonical ID.
func (a adapter) Resolve(ctx context.Context, id string) (platform.Profile, error) {
	p, err := a.api.Player(ctx, id)
	if err != nil {
		return platform.Profile{}, err
	}
	return platform.Profile{ExternalID: p.ID, Name: p.Name, AvatarURL: p.Avatar, Country: p.Country}, nil
}

func (a adapter) FeedPage(ctx context.Context, kind, externalID string, page int) (platform.PlayPage, error) {
	if kind != model.KindScore {
		return platform.PlayPage{}, fmt.Errorf("beatleader: no %s feed", kind)
	}
	sp, err := a.api.Scores(ctx, externalID, page)
	if errors.Is(err, ErrNotFound) {
		// BeatLeader answers 404 for a player without scores: the end of the
		// listing. A vanished player is caught by Resolve (spec §5.2).
		return platform.PlayPage{}, nil
	}
	if err != nil {
		return platform.PlayPage{}, err
	}
	plays, refused := Plays(sp.Data, a.api.ReplayAllowed)
	return platform.PlayPage{Plays: plays, TotalPages: sp.Metadata.TotalPages(), Refused: refused}, nil
}

func (adapter) ProbeAccess(context.Context, string, string) (string, int64, error) {
	return model.AccessNA, 0, nil
}

func (a adapter) Replay(ctx context.Context, ref platform.ReplayRef) (io.ReadCloser, error) {
	if ref.URL == "" {
		return nil, fmt.Errorf("beatleader: %s %s has no replay URL", ref.Kind, ref.ExternalID)
	}
	return a.api.Replay(ctx, ref.URL)
}

func (a adapter) Limiters() []platform.Limiter {
	var out []platform.Limiter
	for _, l := range []*Limiter{a.apiL, a.cdnL} {
		if l != nil {
			out = append(out, l)
		}
	}
	return out
}

// FeedLimiter: listings hit the API host.
func (a adapter) FeedLimiter(string) string { return a.apiName() }

// ReplayLimiter: most replay downloads hit the API host (replays-storage,
// otherreplays); the CDN limiter's Wait covers the rest (spec §5.3).
func (a adapter) ReplayLimiter(string) string { return a.apiName() }

func (a adapter) apiName() string {
	if a.apiL == nil {
		return ""
	}
	return a.apiL.Name()
}

// Plays converts a scores page into neutral plays (spec §4.5, §4.6). A replay
// whose URL is not allowlisted is dropped — the play is kept without one —
// and counted in the second result.
func Plays(items []Score, allowed func(string) bool) ([]platform.Play, int) {
	out := make([]platform.Play, 0, len(items))
	refused := 0
	for _, s := range items {
		pl := play(s)
		pl.Kind, pl.EndType, pl.PersonalBest = model.KindScore, model.EndClear, true
		if s.Replay != "" {
			if allowed(s.Replay) {
				pl.HasReplay, pl.ReplayURL = true, s.Replay
			} else {
				refused++
			}
		}
		out = append(out, pl)
	}
	return out, refused
}

// play maps the fields scores and attempts share.
func play(s Score) platform.Play {
	lb, d := s.Leaderboard, s.Leaderboard.Difficulty
	stars := 0.0
	if d.Stars != nil {
		stars = *d.Stars
	}
	return platform.Play{
		Leaderboard: platform.LeaderboardData{
			ExternalID: cmp.Or(lb.ID, s.LeaderboardID), SongHash: lb.Song.Hash, SongName: lb.Song.Name,
			SongSubName: lb.Song.SubName, SongAuthor: lb.Song.Author, Mapper: lb.Song.Mapper,
			Difficulty: d.Value, DifficultyRaw: d.DifficultyName, GameMode: d.ModeName, CoverURL: lb.Song.CoverImage,
			Status: status(d.Status), Stars: stars, MaxScore: d.MaxScore,
		},
		ExternalID: strconv.FormatInt(s.ID, 10), Rank: s.Rank, ModifiedScore: s.ModifiedScore,
		UnmodifiedScore: s.BaseScore, Accuracy: s.Accuracy, PP: s.PP, Mods: s.Modifiers, FullCombo: s.FullCombo,
		MissedNotes: s.MissedNotes, BadCuts: s.BadCuts, MaxCombo: s.MaxCombo, HMD: HMDName(s.HMD),
		SetAt: cmp.Or(s.Timeset, s.Timepost).Time(),
	}
}

// status maps BeatLeader's difficulty status onto the stored vocabulary.
func status(v int) string {
	switch v {
	case 3:
		return "RANKED"
	case 2:
		return "QUALIFIED"
	}
	return "UNRANKED"
}
