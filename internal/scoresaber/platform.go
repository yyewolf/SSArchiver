package scoresaber

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// API is the part of the ScoreSaber client the adapter uses (*Client
// implements it; tests pass fakes).
type API interface {
	Player(ctx context.Context, id string) (Player, error)
	Scores(ctx context.Context, playerID string, page int) (ScorePage, error)
	Replay(ctx context.Context, scoreID int64) (io.ReadCloser, error)
}

var (
	profileURLRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?scoresaber\.com/u/([0-9]{1,32})(?:[/?#].*)?$`)
	idRe         = regexp.MustCompile(`^[0-9]{1,32}$`)
)

// NewPlatform is ScoreSaber's registry entry: the legacy platform (spec §4.1).
// l may be nil (tests): the platform is then not rate limited.
func NewPlatform(api API, l *Limiter) platform.Platform {
	return platform.Platform{
		Name: model.PlatformScoreSaber, Slug: "ss", DisplayName: "ScoreSaber", Priority: 0, Legacy: true,
		BSOR: true, ReplayExt: ".dat", ImageHosts: []string{"https://cdn.scoresaber.com"},
		ProfileURL: func(id string) string { return "https://scoresaber.com/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := profileURLRe.FindStringSubmatch(strings.TrimSpace(in))
			if m == nil {
				return "", false
			}
			return m[1], true
		},
		ValidID: idRe.MatchString,
		Feeds:   []platform.FeedSpec{{Kind: model.KindScore}},
		Adapter: adapter{api: api, limiter: l},
	}
}

type adapter struct {
	api     API
	limiter *Limiter
}

func (a adapter) Resolve(ctx context.Context, id string) (platform.Profile, error) {
	p, err := a.api.Player(ctx, id)
	if err != nil {
		return platform.Profile{}, err
	}
	return platform.Profile{ExternalID: p.ID, Name: p.Name, AvatarURL: p.Avatar, Country: p.Country}, nil
}

func (a adapter) FeedPage(ctx context.Context, kind, externalID string, page int) (platform.PlayPage, error) {
	if kind != model.KindScore {
		return platform.PlayPage{}, fmt.Errorf("scoresaber: no %s feed", kind)
	}
	sp, err := a.api.Scores(ctx, externalID, page)
	if err != nil {
		return platform.PlayPage{}, err
	}
	return platform.PlayPage{Plays: Plays(sp.Data), TotalPages: sp.Metadata.TotalPages}, nil
}

func (adapter) ProbeAccess(context.Context, string, string) (string, int64, error) {
	return model.AccessNA, 0, nil
}

func (a adapter) Replay(ctx context.Context, ref platform.ReplayRef) (io.ReadCloser, error) {
	id, err := strconv.ParseInt(ref.ExternalID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("scoresaber: replay: invalid score id %q", ref.ExternalID)
	}
	return a.api.Replay(ctx, id)
}

func (a adapter) Limiters() []platform.Limiter {
	if a.limiter == nil {
		return nil
	}
	return []platform.Limiter{a.limiter}
}

func (a adapter) FeedLimiter(string) string { return a.limiterName() }

func (a adapter) ReplayLimiter(string) string { return a.limiterName() }

func (a adapter) limiterName() string {
	if a.limiter == nil {
		return ""
	}
	return a.limiter.Name()
}

// Plays converts a page of ScoreSaber scores into neutral plays.
func Plays(items []ScoreItem) []platform.Play {
	out := make([]platform.Play, 0, len(items))
	for _, it := range items {
		sc, lb := it.Score, it.Leaderboard
		out = append(out, platform.Play{
			Leaderboard: platform.LeaderboardData{
				ExternalID: strconv.FormatInt(lb.ID, 10), SongHash: lb.Map.Hash, SongName: lb.Map.SongName,
				SongSubName: lb.Map.SongSubName, SongAuthor: lb.Map.SongAuthorName, Mapper: lb.Map.LevelAuthorName,
				Difficulty: lb.Difficulty.Difficulty, DifficultyRaw: lb.Difficulty.RawDifficulty, GameMode: lb.Difficulty.GameMode,
				CoverURL: lb.Map.CoverURL, Status: lb.Realm.LeaderboardStatus, Stars: lb.Realm.Stars, MaxScore: lb.MaxScore,
			},
			Kind: model.KindScore, EndType: model.EndClear, ExternalID: strconv.FormatInt(sc.ID, 10),
			Rank: sc.Rank, ModifiedScore: sc.ModifiedScore, UnmodifiedScore: sc.UnmodifiedScore, Accuracy: sc.Accuracy, PP: sc.PP,
			Mods: strings.Join(sc.Mods, ","), FullCombo: sc.FullCombo, MissedNotes: sc.MissedNotes, BadCuts: sc.BadCuts,
			MaxCombo: sc.MaxCombo, HMD: sc.Device.HMD, PersonalBest: sc.PersonalBest, SetAt: sc.CreatedAt.UTC(),
			HasReplay: sc.HasReplay,
			Profile:   &platform.Profile{ExternalID: sc.Player.ID, Name: sc.Player.Name, AvatarURL: sc.Player.Avatar, Country: sc.Player.Country},
		})
	}
	return out
}
