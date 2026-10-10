package scoresaber_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

type stubAPI struct {
	page       scoresaber.ScorePage
	replayID   int64
	playerCall string
}

func (s *stubAPI) Player(_ context.Context, id string) (scoresaber.Player, error) {
	s.playerCall = id
	return scoresaber.Player{ID: id, Name: "Alice", Avatar: "a.jpg", Country: "FR"}, nil
}

func (s *stubAPI) Scores(context.Context, string, int) (scoresaber.ScorePage, error) {
	return s.page, nil
}

func (s *stubAPI) Replay(_ context.Context, id int64) (io.ReadCloser, error) {
	s.replayID = id
	return io.NopCloser(strings.NewReader("r")), nil
}

func TestPlatformDescriptor(t *testing.T) {
	p := scoresaber.NewPlatform(&stubAPI{}, nil)
	if _, err := platform.NewRegistry(p); err != nil {
		t.Fatalf("descriptor must validate: %v", err)
	}
	if p.Name != model.PlatformScoreSaber || p.Slug != "ss" || !p.Legacy || p.ReplayExt != ".dat" || !p.BSOR || p.ProfileURL("42") != "https://scoresaber.com/u/42" {
		t.Fatalf("descriptor = %+v", p)
	}
}

func TestParseRefs(t *testing.T) { // the 2026-10-08 plan's Review Focus #5, now on the adapter
	p := scoresaber.NewPlatform(&stubAPI{}, nil)
	for in, want := range map[string]string{
		"https://scoresaber.com/u/76561198059961776?page=2&sort=recent": "76561198059961776",
		"scoresaber.com/u/76561198059961776/":                           "76561198059961776",
		"http://www.scoresaber.com/u/42#top":                            "42",
	} {
		if got, ok := p.ParseURL(in); !ok || got != want {
			t.Errorf("ParseURL(%q) = %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"https://beatleader.com/u/42", "https://scoresaber.com/leaderboard/42", "not a link"} {
		if _, ok := p.ParseURL(in); ok {
			t.Errorf("ParseURL(%q) accepted", in)
		}
	}
	if !p.ValidID("76561198059961776") || p.ValidID("12a") || p.ValidID("") {
		t.Error("ValidID must accept digits only")
	}
}

func TestAdapterConvertsPages(t *testing.T) {
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	api := &stubAPI{page: scoresaber.ScorePage{
		Data: []scoresaber.ScoreItem{{
			Score: scoresaber.Score{
				ID: 5001, Rank: 3, ModifiedScore: 900, UnmodifiedScore: 950, Accuracy: 0.9, PP: 300,
				Mods: []string{"DA", "FS"}, FullCombo: true, MaxCombo: 500, HasReplay: true, PersonalBest: true, CreatedAt: at,
				Player: scoresaber.Player{ID: "1001", Name: "Alice"}, Device: scoresaber.Device{HMD: "Quest 3"},
			},
			Leaderboard: scoresaber.Leaderboard{
				ID: 1001, MaxScore: 1000,
				Map:        scoresaber.Map{Hash: "ABC", SongName: "Song", CoverURL: "c.png"},
				Difficulty: scoresaber.Difficulty{Difficulty: 9, GameMode: "SoloStandard", RawDifficulty: "_ExpertPlus_SoloStandard"},
				Realm:      scoresaber.Realm{LeaderboardStatus: "RANKED", Stars: 10.5},
			},
		}},
		Metadata: scoresaber.PageMeta{TotalPages: 7},
	}}
	p := scoresaber.NewPlatform(api, nil)
	pg, err := p.Adapter.FeedPage(context.Background(), model.KindScore, "1001", 1)
	if err != nil || pg.TotalPages != 7 || len(pg.Plays) != 1 {
		t.Fatalf("page = %+v %v", pg, err)
	}
	pl := pg.Plays[0]
	if pl.Kind != model.KindScore || pl.EndType != model.EndClear || pl.ExternalID != "5001" || pl.Mods != "DA,FS" ||
		!pl.SetAt.Equal(at) || !pl.HasReplay || pl.ReplayURL != "" || pl.Profile == nil || pl.Profile.Name != "Alice" ||
		pl.Leaderboard.ExternalID != "1001" || pl.Leaderboard.GameMode != "SoloStandard" || pl.Leaderboard.Status != "RANKED" {
		t.Fatalf("play = %+v", pl)
	}
	if _, err := p.Adapter.FeedPage(context.Background(), model.KindAttempt, "1001", 1); err == nil {
		t.Fatal("ScoreSaber has no attempt feed")
	}
	if _, err := p.Adapter.Replay(context.Background(), platform.ReplayRef{Kind: model.KindScore, ExternalID: "5001"}); err != nil || api.replayID != 5001 {
		t.Fatalf("replay by score ID: %v %d", err, api.replayID)
	}
	if access, _, err := p.Adapter.ProbeAccess(context.Background(), model.KindScore, "1001"); err != nil || access != model.AccessNA {
		t.Fatalf("ProbeAccess = %q %v", access, err)
	}
	prof, err := p.Adapter.Resolve(context.Background(), "1001")
	if err != nil || prof.ExternalID != "1001" || prof.AvatarURL != "a.jpg" {
		t.Fatalf("Resolve = %+v %v", prof, err)
	}
}
