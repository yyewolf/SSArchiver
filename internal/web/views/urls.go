package views

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func PlayerScoresURL(playerID string, f service.ScoreFilter, page int) string {
	q := url.Values{}
	if f.Search != "" {
		q.Set("q", f.Search)
	}
	if f.State != "" {
		q.Set("state", f.State)
	}
	if f.RankedOnly {
		q.Set("ranked", "1")
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	u := "/p/" + url.PathEscape(playerID)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

func EmbedSnippet(embedURL string) string {
	return fmt.Sprintf(`<iframe src="%s" width="960" height="540" allow="fullscreen" loading="lazy" style="border:0"></iframe>`, embedURL)
}

// ViewerSrc is the same-origin ArcViewer URL that loads one archived replay.
// When the instance enables headset rendering, ArcViewer's settingsOverride
// parameter applies the admin's choices over each visitor's own viewer
// settings (the visitor can still veto them inside the viewer UI).
func ViewerSrc(base string, id int64, autoplay, loop, hideUI bool, st service.Settings) string {
	v := url.Values{}
	v.Set("replayURL", base+ReplayPath(id))
	v.Set("noProxy", "true")
	if autoplay {
		v.Set("autoPlay", "true")
	}
	if loop {
		v.Set("loop", "true")
	}
	if hideUI {
		v.Set("uiOff", "true")
	}
	if st.ViewerShowHeadset {
		r, g, b, ok := service.ParseHexColor(st.ViewerHeadsetColor)
		if !ok {
			r, g, b = 0.529, 0.529, 0.529
		}
		round := func(f float64) float64 { return math.Round(f*1000) / 1000 }
		override := struct {
			Bools  map[string]bool    `json:"Bools"`
			Ints   map[string]int     `json:"Ints"`
			Floats map[string]float64 `json:"Floats"`
		}{
			// All three dictionaries must be present (empty is fine):
			// Newtonsoft leaves omitted ones null and ArcViewer dereferences
			// them when overrides are active.
			Bools: map[string]bool{"showheadset": true},
			Ints:  map[string]int{},
			Floats: map[string]float64{
				"headsetalpha":   round(st.ViewerHeadsetAlpha),
				"headsetcolor.r": round(r),
				"headsetcolor.g": round(g),
				"headsetcolor.b": round(b),
			},
		}
		if data, err := json.Marshal(override); err == nil {
			v.Set("settingsOverride", string(data))
		}
	}
	return "/viewer/?" + v.Encode()
}

func SongTitle(s *model.Score) string {
	if s.Leaderboard == nil {
		return fmt.Sprintf("Leaderboard %d", s.LeaderboardID)
	}
	if s.Leaderboard.SongSubName != "" {
		return s.Leaderboard.SongName + " " + s.Leaderboard.SongSubName
	}
	return s.Leaderboard.SongName
}

func SongAuthor(s *model.Score) string {
	if s.Leaderboard == nil {
		return ""
	}
	return s.Leaderboard.SongAuthor
}

func Mapper(s *model.Score) string {
	if s.Leaderboard == nil {
		return ""
	}
	return s.Leaderboard.Mapper
}

func PlayerName(s *model.Score) string {
	if s.Player == nil {
		return s.PlayerID
	}
	return s.Player.Name
}

func CoverURL(s *model.Score) string {
	if s.Leaderboard == nil {
		return ""
	}
	return s.Leaderboard.CoverURL
}

func ScoreSummary(s *model.Score) string {
	parts := []string{Percent(s.Accuracy), "#" + strconv.Itoa(s.Rank)}
	if s.FullCombo {
		parts = append(parts, "FC")
	} else {
		parts = append(parts, fmt.Sprintf("%d mistakes", s.MissedNotes+s.BadCuts))
	}
	if s.PP > 0 {
		parts = append(parts, PP(s.PP))
	}
	return strings.Join(parts, " · ")
}
