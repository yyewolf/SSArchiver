package scoresaber

import "time"

type Player struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Avatar  string `json:"avatar"`
	Country string `json:"country"`
}

type ScorePage struct {
	Data     []ScoreItem `json:"data"`
	Metadata PageMeta    `json:"metadata"`
}

type PageMeta struct {
	Page         int `json:"page"`
	ItemsPerPage int `json:"itemsPerPage"`
	TotalItems   int `json:"totalItems"`
	TotalPages   int `json:"totalPages"`
}

type ScoreItem struct {
	Score       Score       `json:"score"`
	Leaderboard Leaderboard `json:"leaderboard"`
}

type Score struct {
	ID              int64     `json:"id"`
	Rank            int       `json:"rank"`
	UnmodifiedScore int64     `json:"unmodifiedScore"`
	ModifiedScore   int64     `json:"modifiedScore"`
	Accuracy        float64   `json:"accuracy"`
	PP              float64   `json:"pp"`
	Mods            []string  `json:"mods"`
	BadCuts         int       `json:"badCuts"`
	MissedNotes     int       `json:"missedNotes"`
	MaxCombo        int       `json:"maxCombo"`
	FullCombo       bool      `json:"fullCombo"`
	HasReplay       bool      `json:"hasReplay"`
	PersonalBest    bool      `json:"personalBest"`
	CreatedAt       time.Time `json:"createdAt"`
	Player          Player    `json:"player"`
	Device          Device    `json:"device"`
}

type Device struct {
	HMD string `json:"hmd"`
}

type Leaderboard struct {
	ID         int64      `json:"id"`
	Map        Map        `json:"map"`
	Difficulty Difficulty `json:"difficulty"`
	MaxScore   int64      `json:"maxScore"`
	Realm      Realm      `json:"realm"`
}

type Map struct {
	Hash            string `json:"hash"`
	SongName        string `json:"songName"`
	SongSubName     string `json:"songSubName"`
	SongAuthorName  string `json:"songAuthorName"`
	LevelAuthorName string `json:"levelAuthorName"`
	CoverURL        string `json:"coverUrl"`
}

type Difficulty struct {
	Difficulty    int    `json:"difficulty"`
	GameMode      string `json:"gameMode"`
	RawDifficulty string `json:"rawDifficulty"`
}

type Realm struct {
	LeaderboardStatus string  `json:"leaderboardStatus"`
	Stars             float64 `json:"stars"`
}
