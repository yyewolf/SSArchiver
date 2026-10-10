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
	Attempts(ctx context.Context, playerID string, page, count int) (ScorePage, error)
	Replay(ctx context.Context, url string) (io.ReadCloser, error)
	ReplayAllowed(url string) bool
	ScoreReplay(url string) bool
}

var (
	profileURLRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?beatleader\.(?:com|xyz)/u/([A-Za-z0-9_.-]{1,64})/?(?:[?#].*)?$`)
	idRe         = regexp.MustCompile(`^[0-9]{1,32}$`)
)

// attemptsHint is shown while a player's attempt history is private (spec §6.3).
var attemptsHint = &platform.Hint{
	Title: "Attempt history is private on BeatLeader",
	Intro: "SSArchiver can only archive attempts when the player makes their history public:",
	Steps: []string{
		"Sign in on beatleader.com with this account.",
		"Open Settings → Scores.",
		"Turn on Public history (auto-synced).",
		"Reload the page and check the switch is still on (BeatLeader can show it on even when saving failed).",
	},
	LinkText: "Open BeatLeader settings",
	LinkURL:  "https://beatleader.com/settings",
	Note: "SSArchiver also re-checks every 24 hours. Old attempt replays are dropped by BeatLeader over time, " +
		"so the sooner this is on, the more can be saved.",
}

// NewPlatform is BeatLeader's registry entry (spec §5.3). Nil limiters
// (tests) mean no client-side rate limiting.
func NewPlatform(api API, apiL, cdnL *Limiter) platform.Platform {
	return platform.Platform{
		Name: Name, Slug: "bl", DisplayName: "BeatLeader", Priority: 10, PBOnly: true, BSOR: true, ReplayExt: ".bsor",
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
		Feeds: []platform.FeedSpec{
			{Kind: model.KindScore},
			{Kind: model.KindAttempt, Optional: true, NeedsAccess: true, AccessHint: attemptsHint},
		},
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
	switch kind {
	case model.KindScore:
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
	case model.KindAttempt:
		sp, err := a.api.Attempts(ctx, externalID, page, ScoresPageSize)
		if err != nil {
			return platform.PlayPage{}, err // 401 = private history: platform.ErrUnauthorized
		}
		plays, skipped, refused := AttemptPlays(sp.Data, a.api.ReplayAllowed, a.api.ScoreReplay)
		return platform.PlayPage{Plays: plays, TotalPages: sp.Metadata.TotalPages(), Refused: refused, Skipped: skipped}, nil
	}
	return platform.PlayPage{}, fmt.Errorf("beatleader: no %s feed", kind)
}

// ProbeAccess reads one attempt: 200 means the history is public, 401 private
// (spec §2.2). The score feed needs no probe.
func (a adapter) ProbeAccess(ctx context.Context, kind, externalID string) (string, int64, error) {
	if kind != model.KindAttempt {
		return model.AccessNA, 0, nil
	}
	sp, err := a.api.Attempts(ctx, externalID, 1, 1)
	switch {
	case errors.Is(err, ErrUnauthorized):
		return model.AccessPrivate, 0, nil
	case err != nil:
		return "", 0, err
	}
	return model.AccessPublic, int64(sp.Metadata.Total), nil
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

// attemptEnds maps BeatLeader's endType onto the stored vocabulary (spec §4.4).
var attemptEnds = map[int]string{
	1: model.EndClear, 2: model.EndFail, 3: model.EndRestart, 4: model.EndQuit, 5: model.EndPractice,
}

// AttemptPlays converts an attempts page (spec §4.6). A clear whose replay is
// a score replay is the PB the scores feed archives: it is skipped (second
// result). A replay off the allowlist is dropped and counted (third result).
func AttemptPlays(items []Score, allowed, scoreReplay func(string) bool) (plays []platform.Play, skipped, refused int) {
	for _, s := range items {
		if s.EndType == 1 && s.Replay != "" && scoreReplay(s.Replay) {
			skipped++
			continue
		}
		pl := play(s)
		end, ok := attemptEnds[s.EndType]
		if !ok {
			end = model.EndUnknown
		}
		t := s.Time
		pl.Kind, pl.EndType, pl.EndTime, pl.PersonalBest = model.KindAttempt, end, &t, false
		pl.SetAt = s.Timepost.Time()
		if s.Replay != "" {
			if allowed(s.Replay) {
				pl.HasReplay, pl.ReplayURL = true, s.Replay
			} else {
				refused++
			}
		}
		plays = append(plays, pl)
	}
	return plays, skipped, refused
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
