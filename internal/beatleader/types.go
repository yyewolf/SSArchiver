// Package beatleader is a minimal client for BeatLeader's public API (spec
// §2.1, §5.3) with client-side rate limiters that mirror the server's 10 s
// window. It holds no application logic: the adapter in platform.go turns
// its payloads into neutral platform.Play values.
package beatleader

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Player struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Avatar  string `json:"avatar"`
	Country string `json:"country"`
}

type ScorePage struct {
	Metadata Meta    `json:"metadata"`
	Data     []Score `json:"data"`
}

type Meta struct {
	Page         int `json:"page"`
	ItemsPerPage int `json:"itemsPerPage"`
	Total        int `json:"total"`
}

// TotalPages is the number of pages of the listing.
func (m Meta) TotalPages() int {
	per := m.ItemsPerPage
	if per <= 0 {
		per = ScoresPageSize
	}
	return (m.Total + per - 1) / per
}

// Score is one entry of a scores listing (one per leaderboard: the current
// personal best).
type Score struct {
	ID            int64       `json:"id"`
	BaseScore     int64       `json:"baseScore"`
	ModifiedScore int64       `json:"modifiedScore"`
	Accuracy      float64     `json:"accuracy"`
	PP            float64     `json:"pp"`
	Rank          int         `json:"rank"`
	Modifiers     string      `json:"modifiers"`
	BadCuts       int         `json:"badCuts"`
	MissedNotes   int         `json:"missedNotes"`
	FullCombo     bool        `json:"fullCombo"`
	MaxCombo      int         `json:"maxCombo"`
	HMD           int         `json:"hmd"`
	Timeset       UnixTime    `json:"timeset"`
	Timepost      UnixTime    `json:"timepost"`
	EndType       int         `json:"endType"` // attempts: unknown(0) clear(1) fail(2) restart(3) quit(4) practice(5)
	Time          float64     `json:"time"`    // attempts: seconds into the song when the run ended
	LeaderboardID string      `json:"leaderboardId"`
	Replay        string      `json:"replay"` // null decodes to ""
	Leaderboard   Leaderboard `json:"leaderboard"`
}

type Leaderboard struct {
	ID         string     `json:"id"`
	Song       Song       `json:"song"`
	Difficulty Difficulty `json:"difficulty"`
}

type Song struct {
	Hash       string `json:"hash"` // lowercase
	Name       string `json:"name"`
	SubName    string `json:"subName"`
	Author     string `json:"author"`
	Mapper     string `json:"mapper"`
	CoverImage string `json:"coverImage"`
}

type Difficulty struct {
	Value          int      `json:"value"`    // 1..9, same scale as ScoreSaber
	ModeName       string   `json:"modeName"` // "Standard", "OneSaber", …
	DifficultyName string   `json:"difficultyName"`
	Status         int      `json:"status"` // unranked(0) nominated(1) qualified(2) ranked(3) …
	Stars          *float64 `json:"stars"`
	MaxScore       int64    `json:"maxScore"`
}

// UnixTime is a unix-seconds timestamp that BeatLeader sends as a number or as
// a numeric string (scores' timeset); null and "" decode to zero.
type UnixTime int64

func (u *UnixTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*u = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("beatleader: bad timestamp %s: %w", b, err)
	}
	*u = UnixTime(n)
	return nil
}

// Time is the timestamp in UTC (zero time.Time for zero).
func (u UnixTime) Time() time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(int64(u), 0).UTC()
}
