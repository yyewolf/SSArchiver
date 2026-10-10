package views

import (
	"context"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	reg, err := platform.NewRegistry(scoresaber.NewPlatform(nil, nil), testutil.NewFakePlatform().Platform())
	if err != nil {
		t.Fatal(err)
	}
	return WithPlatforms(context.Background(), reg)
}

func ssRow(id string) *model.Score {
	return &model.Score{Platform: model.PlatformScoreSaber, Kind: model.KindScore, ExternalID: id}
}

func tpRow(kind, id string) *model.Score {
	return &model.Score{Platform: "testplat", Kind: kind, ExternalID: id}
}

func TestURLs(t *testing.T) {
	ctx := testCtx(t)
	f := service.ScoreFilter{Search: "ghost rule", State: service.FilterArchived, RankedOnly: true}
	if got := PlayerScoresURL("1001", f, 3); got != "/p/1001?page=3&q=ghost+rule&ranked=1&state=archived" {
		t.Errorf("PlayerScoresURL = %s", got)
	}
	if got := PlayerScoresURL("1001", service.ScoreFilter{}, 1); got != "/p/1001" {
		t.Errorf("PlayerScoresURL(empty) = %s", got)
	}
	src := ViewerSrc(ctx, "https://r.example.com", ssRow("42"), true, false, true, service.DefaultSettings)
	if src != "/viewer/?autoPlay=true&noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2F42.dat&uiOff=true" {
		t.Errorf("ViewerSrc = %s", src)
	}
	if got := ViewerSrc(ctx, "https://r.example.com", tpRow(model.KindScore, "t1"), false, false, false, service.DefaultSettings); got != "/viewer/?noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2Ftp%2Ft1.tpr" {
		t.Errorf("ViewerSrc(testplat) = %s", got)
	}
	if got := EmbedSnippet("https://r.example.com/embed/42"); got != `<iframe src="https://r.example.com/embed/42" width="960" height="540" allow="fullscreen" loading="lazy" style="border:0"></iframe>` {
		t.Errorf("EmbedSnippet = %s", got)
	}
}

func TestViewerSrcHeadsetOverride(t *testing.T) {
	ctx := testCtx(t)
	st := service.DefaultSettings
	st.ViewerShowHeadset = true
	st.ViewerHeadsetColor = "#Ff0080"
	st.ViewerHeadsetAlpha = 0.4
	src := ViewerSrc(ctx, "https://r.example.com", ssRow("42"), false, false, false, st)
	want := "/viewer/?noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2F42.dat" +
		"&settingsOverride=%7B%22Bools%22%3A%7B%22showheadset%22%3Atrue%7D%2C%22Ints%22%3A%7B%7D%2C%22Floats%22%3A%7B%22headsetalpha%22%3A0.4%2C%22headsetcolor.b%22%3A0.502%2C%22headsetcolor.g%22%3A0%2C%22headsetcolor.r%22%3A1%7D%7D"
	if src != want {
		t.Errorf("ViewerSrc = %s, want %s", src, want)
	}
	if got := ViewerSrc(ctx, "https://r.example.com", ssRow("42"), false, false, false, service.DefaultSettings); got != "/viewer/?noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2F42.dat" {
		t.Errorf("ViewerSrc(default) = %s", got)
	}
}

func TestRowLinks(t *testing.T) {
	ctx := testCtx(t)
	for got, want := range map[string]string{
		ScoreURL(ctx, ssRow("42")):                         "/s/42",
		ReplayPath(ctx, ssRow("42")):                       "/r/42.dat",
		EmbedPath(ctx, ssRow("42")):                        "/embed/42",
		ScoreURL(ctx, tpRow(model.KindScore, "t1")):        "/s/tp/t1",
		ScoreURL(ctx, tpRow(model.KindAttempt, "a1")):      "/s/tp/attempt/a1",
		ReplayPath(ctx, tpRow(model.KindAttempt, "a1")):    "/r/tp/attempt/a1.tpr",
		EmbedPath(ctx, tpRow(model.KindScore, "t1")):       "/embed/tp/t1",
		ReplayExt(ctx, tpRow(model.KindScore, "t1")):       ".tpr",
		AccountPath(ctx, model.PlatformScoreSaber, "1001"): "/p/ss/1001",
		ProfileURL(ctx, "testplat", "abc"):                 "https://tp.example/u/abc",
		PlatformName(ctx, "testplat"):                      "TestPlat",
		PlatformName(ctx, "gone"):                          "gone",
		ScoreURL(context.Background(), ssRow("42")):        "",
		PlatformName(context.Background(), "testplat"):     "testplat",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestScoreText(t *testing.T) {
	s := &model.Score{
		ID: 1, Rank: 3, Accuracy: 0.9728, FullCombo: true, PP: 0,
		Leaderboard: &model.Leaderboard{SongName: "Hell of a time", SongSubName: "(Live)", SongAuthor: "Quadeca", Mapper: "oermergeesh", CoverURL: "https://cdn.scoresaber.com/covers/x.png"},
		Player:      &model.Player{Name: "oermer"},
	}
	if SongTitle(s) != "Hell of a time (Live)" || SongAuthor(s) != "Quadeca" || Mapper(s) != "oermergeesh" || PlayerName(s) != "oermer" || CoverURL(s) == "" {
		t.Fatal("text helpers wrong")
	}
	if got := ScoreSummary(s); got != "97.28% · #3 · FC" {
		t.Errorf("ScoreSummary = %q", got)
	}
	s.FullCombo, s.MissedNotes, s.BadCuts, s.PP = false, 2, 1, 312.5
	if got := ScoreSummary(s); got != "97.28% · #3 · 3 mistakes · 312.50pp" {
		t.Errorf("ScoreSummary = %q", got)
	}
	bare := &model.Score{LeaderboardID: 7, PlayerID: "9"}
	if SongTitle(bare) != "Leaderboard 7" || PlayerName(bare) != "9" || CoverURL(bare) != "" {
		t.Fatal("helpers must tolerate missing preloads")
	}
}
