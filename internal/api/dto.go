package api

import (
	"strconv"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type ReplayCounts struct {
	Scores   int64 `json:"scores" doc:"All stored scores"`
	Archived int64 `json:"archived"`
	Pending  int64 `json:"pending"`
	Failed   int64 `json:"failed"`
	Gone     int64 `json:"gone" doc:"Pruned by ScoreSaber before they could be archived"`
}

type Backfill struct {
	State      string `json:"state" enum:"pending,running,done"`
	NextPage   int    `json:"next_page"`
	TotalPages int    `json:"total_pages"`
}

type Feed struct {
	Kind         string     `json:"kind" enum:"score,attempt"`
	Enabled      bool       `json:"enabled"`
	Access       string     `json:"access" enum:"n/a,unknown,public,private"`
	StartedAt    time.Time  `json:"started_at"`
	LastPolledAt *time.Time `json:"last_polled_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Backfill     Backfill   `json:"backfill"`
}

type Identity struct {
	Platform   string    `json:"platform" example:"scoresaber"`
	ID         string    `json:"id" doc:"The player's ID on that platform" example:"76561198038925092"`
	ProfileURL string    `json:"profile_url"`
	Enabled    bool      `json:"enabled"`
	LinkedAt   time.Time `json:"linked_at"`
	LastError  string    `json:"last_error,omitempty"`
	Feeds      []Feed    `json:"feeds"`
}

type Player struct {
	ID           string       `json:"id" example:"76561198059961776"`
	Name         string       `json:"name"`
	AvatarURL    string       `json:"avatar_url"`
	Country      string       `json:"country"`
	Enabled      bool         `json:"enabled"`
	AddedAt      time.Time    `json:"added_at"`
	LastPolledAt *time.Time   `json:"last_polled_at,omitempty" deprecated:"true" doc:"Deprecated: use identities[].feeds"`
	LastError    string       `json:"last_error,omitempty" deprecated:"true" doc:"Deprecated: use identities[] and their feeds"`
	Backfill     Backfill     `json:"backfill" deprecated:"true" doc:"Deprecated: use identities[].feeds"`
	Identities   []Identity   `json:"identities"`
	Replays      ReplayCounts `json:"replays"`
	URL          string       `json:"url"`
}

type Leaderboard struct {
	ID            int64   `json:"id"`
	SongHash      string  `json:"song_hash"`
	SongName      string  `json:"song_name"`
	SongSubName   string  `json:"song_sub_name"`
	SongAuthor    string  `json:"song_author"`
	Mapper        string  `json:"mapper"`
	Difficulty    string  `json:"difficulty" example:"Expert+"`
	DifficultyRaw string  `json:"difficulty_raw"`
	GameMode      string  `json:"game_mode"`
	CoverURL      string  `json:"cover_url"`
	Status        string  `json:"status" example:"RANKED"`
	Stars         float64 `json:"stars"`
}

type Replay struct {
	State       string     `json:"state" enum:"none,pending,archived,gone,failed"`
	Size        int64      `json:"size,omitempty"`
	SHA256      string     `json:"sha256,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	DownloadURL string     `json:"download_url,omitempty"`
	EmbedURL    string     `json:"embed_url,omitempty"`
}

type Score struct {
	ID          int64        `json:"id"`
	PlayerID    string       `json:"player_id"`
	Leaderboard *Leaderboard `json:"leaderboard,omitempty"`
	Rank        int          `json:"rank"`
	Score       int64        `json:"score"`
	Accuracy    float64      `json:"accuracy" doc:"0..1"`
	PP          float64      `json:"pp"`
	Mods        []string     `json:"mods"`
	FullCombo   bool         `json:"full_combo"`
	MissedNotes int          `json:"missed_notes"`
	BadCuts     int          `json:"bad_cuts"`
	MaxCombo    int          `json:"max_combo"`
	HMD         string       `json:"hmd"`
	SetAt       time.Time    `json:"set_at"`
	Replay      Replay       `json:"replay"`
	URL         string       `json:"url"`
}

func difficultyName(d int) string {
	switch d {
	case 1:
		return "Easy"
	case 3:
		return "Normal"
	case 5:
		return "Hard"
	case 7:
		return "Expert"
	case 9:
		return "Expert+"
	}
	return "Unknown"
}

func playerDTO(base string, reg *platform.Registry, p service.PlayerSummary) Player {
	sync := p.Sync()
	out := Player{
		ID: p.ID, Name: p.Name, AvatarURL: p.AvatarURL, Country: p.Country, Enabled: p.Enabled,
		AddedAt: p.AddedAt, LastPolledAt: sync.LastPolledAt, LastError: p.Error(),
		Backfill:   Backfill{State: sync.BackfillState, NextPage: sync.BackfillPage, TotalPages: sync.BackfillTotalPages},
		Replays:    ReplayCounts{Scores: p.Counts.Scores, Archived: p.Counts.Archived, Pending: p.Counts.Pending, Failed: p.Counts.Failed, Gone: p.Counts.Gone},
		URL:        base + "/p/" + p.ID,
		Identities: make([]Identity, 0, len(p.Identities)),
	}
	for _, id := range p.Identities {
		dto := Identity{Platform: id.Platform, ID: id.ExternalID, Enabled: id.Enabled, LinkedAt: id.LinkedAt, LastError: id.LastError, Feeds: make([]Feed, 0, len(id.Feeds))}
		if pl, ok := reg.Get(id.Platform); ok {
			dto.ProfileURL = pl.ProfileURL(id.ExternalID)
		}
		for _, f := range id.Feeds {
			dto.Feeds = append(dto.Feeds, Feed{
				Kind: f.Feed, Enabled: f.Enabled, Access: f.Access, StartedAt: f.StartedAt, LastPolledAt: f.LastPolledAt, LastError: f.LastError,
				Backfill: Backfill{State: f.BackfillState, NextPage: f.BackfillPage, TotalPages: f.BackfillTotalPages},
			})
		}
		out.Identities = append(out.Identities, dto)
	}
	return out
}

func scoreDTO(base string, s *model.Score) Score {
	id := strconv.FormatInt(s.ID, 10)
	mods := []string{}
	if s.Mods != "" {
		mods = strings.Split(s.Mods, ",")
	}
	out := Score{
		ID: s.ID, PlayerID: s.PlayerID, Rank: s.Rank, Score: s.ModifiedScore, Accuracy: s.Accuracy, PP: s.PP,
		Mods: mods, FullCombo: s.FullCombo, MissedNotes: s.MissedNotes, BadCuts: s.BadCuts, MaxCombo: s.MaxCombo,
		HMD: s.HMD, SetAt: s.SetAt, Replay: Replay{State: s.ReplayState}, URL: base + "/s/" + id,
	}
	if lb := s.Leaderboard; lb != nil {
		out.Leaderboard = &Leaderboard{
			ID: lb.ID, SongHash: lb.SongHash, SongName: lb.SongName, SongSubName: lb.SongSubName, SongAuthor: lb.SongAuthor,
			Mapper: lb.Mapper, Difficulty: difficultyName(lb.Difficulty), DifficultyRaw: lb.DifficultyRaw, GameMode: lb.GameMode,
			CoverURL: lb.CoverURL, Status: lb.Status, Stars: lb.Stars,
		}
	}
	if s.ReplayState == model.ReplayArchived {
		out.Replay.Size, out.Replay.SHA256, out.Replay.ArchivedAt = s.ReplaySize, s.ReplaySHA256, s.ArchivedAt
		out.Replay.DownloadURL = base + "/r/" + id + ".dat"
		out.Replay.EmbedURL = base + "/embed/" + id
	}
	return out
}
