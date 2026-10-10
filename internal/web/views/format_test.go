package views

import (
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
)

func TestFormat(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	checks := map[string]string{
		Number(1098260):                         "1,098,260",
		Number(-1234):                           "-1,234",
		Number(12):                              "12",
		Percent(0.9728111):                      "97.28%",
		PP(0):                                   "—",
		PP(312.5):                               "312.50pp",
		DifficultyName(9):                       "Expert+",
		DifficultyName(4):                       "Unknown",
		TimeAgo(now.Add(-3*time.Minute), now):   "3m ago",
		TimeAgo(now.Add(-2*time.Second), now):   "just now",
		TimeAgo(now.Add(-40*24*time.Hour), now): "29 Aug 2026",
		TimeAgo(time.Time{}, now):               "never",
		Until(now.Add(90*time.Second), now):     "in 1m",
		Until(now.Add(-time.Second), now):       "due",
		HumanDuration(26 * time.Hour):           "26h 0m",
		HumanBytes(2814210):                     "2.7 MB",
		Initials("oermer"):                      "O",
		Initials("Ghost Rule Fan"):              "GR",
		Initials(""):                            "?",
	}
	for got, want := range checks {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestAttemptText(t *testing.T) {
	for got, want := range map[string]string{
		EndLabel(model.EndFail): "Failed", EndLabel(model.EndQuit): "Quit", EndLabel(model.EndRestart): "Restarted",
		EndLabel(model.EndPractice): "Practice", EndLabel(model.EndClear): "Cleared", EndLabel(model.EndUnknown): "Ended",
		SongTime(new(59.74)): "0:59", SongTime(new(210.65674)): "3:30", SongTime(nil): "—",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	a := &model.Score{Kind: model.KindAttempt, EndType: model.EndFail, EndTime: new(59.74), Accuracy: 0.698989}
	if got := ScoreSummary(a); got != "Failed at 0:59 · 69.90%" {
		t.Errorf("ScoreSummary(attempt) = %q", got)
	}
	if !ViewerPlays(&model.Score{Kind: model.KindScore}) || !ViewerPlays(&model.Score{Kind: model.KindAttempt, EndType: model.EndClear}) {
		t.Error("scores and clears always play")
	}
	if ViewerPlays(a) != AttemptViewer {
		t.Error("runs that ended early play only when the bundled viewer handles them")
	}
}
