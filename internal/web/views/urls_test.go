package views

import (
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func TestURLs(t *testing.T) {
	f := service.ScoreFilter{Search: "ghost rule", State: service.FilterArchived, RankedOnly: true}
	if got := PlayerScoresURL("1001", f, 3); got != "/p/1001?page=3&q=ghost+rule&ranked=1&state=archived" {
		t.Errorf("PlayerScoresURL = %s", got)
	}
	if got := PlayerScoresURL("1001", service.ScoreFilter{}, 1); got != "/p/1001" {
		t.Errorf("PlayerScoresURL(empty) = %s", got)
	}
	src := ViewerSrc("https://r.example.com", 42, true, false, true)
	if src != "/viewer/?autoPlay=true&noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2F42.dat&uiOff=true" {
		t.Errorf("ViewerSrc = %s", src)
	}
	if got := EmbedSnippet("https://r.example.com/embed/42"); got != `<iframe src="https://r.example.com/embed/42" width="960" height="540" allow="fullscreen" loading="lazy" style="border:0"></iframe>` {
		t.Errorf("EmbedSnippet = %s", got)
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
