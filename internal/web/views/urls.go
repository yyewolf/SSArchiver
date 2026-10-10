package views

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

// scoreQuery encodes the player page's filters (page excluded).
func scoreQuery(f service.ScoreFilter) url.Values {
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
	if f.Platform != "" {
		q.Set("platform", f.Platform)
	}
	if f.MinScore != nil {
		q.Set("min_score", strconv.FormatInt(*f.MinScore, 10))
	}
	if f.MaxScore != nil {
		q.Set("max_score", strconv.FormatInt(*f.MaxScore, 10))
	}
	if len(f.Types) > 0 && (len(f.Types) != 1 || f.Types[0] != service.TypeComplete) {
		q.Set("type", strings.Join(f.Types, ","))
	}
	return q
}

func PlayerScoresURL(playerID string, f service.ScoreFilter, page int) string {
	q := scoreQuery(f)
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	u := "/p/" + url.PathEscape(playerID)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// MapPlaysURL is the fragment with every play of one map, filters applied.
func MapPlaysURL(playerID string, f service.ScoreFilter, mapKey string) string {
	q := scoreQuery(f)
	q.Set("key", mapKey)
	return "/p/" + url.PathEscape(playerID) + "/map?" + q.Encode()
}

func EmbedSnippet(embedURL string) string {
	return fmt.Sprintf(`<iframe src="%s" width="960" height="540" allow="fullscreen" loading="lazy" style="border:0"></iframe>`, embedURL)
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
