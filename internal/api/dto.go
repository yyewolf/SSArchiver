package api

import (
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type ReplayCounts struct {
	Scores   int64 `json:"scores" doc:"Stored scores"`
	Archived int64 `json:"archived"`
	Pending  int64 `json:"pending"`
	Failed   int64 `json:"failed"`
	Gone     int64 `json:"gone" doc:"Pruned by the platform before they could be archived"`
}

func replayCounts(c service.Counts) ReplayCounts {
	return ReplayCounts{Scores: c.Scores, Archived: c.Archived, Pending: c.Pending, Failed: c.Failed, Gone: c.Gone}
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
	Platform   string       `json:"platform" example:"scoresaber"`
	ID         string       `json:"id" doc:"The player's ID on that platform" example:"76561198038925092"`
	ProfileURL string       `json:"profile_url"`
	Enabled    bool         `json:"enabled"`
	LinkedAt   time.Time    `json:"linked_at"`
	LastError  string       `json:"last_error,omitempty"`
	Feeds      []Feed       `json:"feeds"`
	Counts     ReplayCounts `json:"counts" doc:"This account's scores"`
}

type Player struct {
	ID           string       `json:"id" example:"k7m2q9x4c1ab"`
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
	ID            int64   `json:"id" doc:"ScoreSaber leaderboard ID; 0 on other platforms"`
	ExternalID    string  `json:"external_id" doc:"The platform's leaderboard ID"`
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
	ID          int64        `json:"id" doc:"ScoreSaber score ID; 0 on other platforms (use platform + external_id)"`
	Platform    string       `json:"platform" example:"beatleader"`
	Kind        string       `json:"kind" enum:"score,attempt"`
	EndType     string       `json:"end_type" enum:"clear,fail,restart,quit,practice,unknown"`
	EndTime     *float64     `json:"end_time,omitempty" doc:"Seconds into the song when an attempt ended"`
	ExternalID  string       `json:"external_id" doc:"The platform's score or attempt ID"`
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
		Replays:    replayCounts(p.Counts),
		URL:        base + "/p/" + p.ID,
		Identities: make([]Identity, 0, len(p.Identities)),
	}
	for _, id := range p.Identities {
		dto := Identity{Platform: id.Platform, ID: id.ExternalID, Enabled: id.Enabled, LinkedAt: id.LinkedAt, LastError: id.LastError, Feeds: make([]Feed, 0, len(id.Feeds)), Counts: replayCounts(id.Scores())}
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

func scoreDTO(base string, reg *platform.Registry, s *model.Score) Score {
	p, _ := reg.Get(s.Platform)
	ref := platform.PlayRef{Platform: s.Platform, Kind: s.Kind, ExternalID: s.ExternalID}
	mods := []string{}
	if s.Mods != "" {
		mods = strings.Split(s.Mods, ",")
	}
	out := Score{
		Platform: s.Platform, Kind: s.Kind, EndType: s.EndType, EndTime: s.EndTime, ExternalID: s.ExternalID,
		PlayerID: s.PlayerID, Rank: s.Rank, Score: s.ModifiedScore, Accuracy: s.Accuracy, PP: s.PP,
		Mods: mods, FullCombo: s.FullCombo, MissedNotes: s.MissedNotes, BadCuts: s.BadCuts, MaxCombo: s.MaxCombo,
		HMD: s.HMD, SetAt: s.SetAt, Replay: Replay{State: s.ReplayState}, URL: base + reg.PlayPath("/s", ref),
	}
	if p.Legacy {
		out.ID = s.ID // internal IDs of other platforms are never exposed
	}
	if lb := s.Leaderboard; lb != nil {
		out.Leaderboard = &Leaderboard{
			ExternalID: lb.ExternalID, SongHash: lb.SongHash, SongName: lb.SongName, SongSubName: lb.SongSubName,
			SongAuthor: lb.SongAuthor, Mapper: lb.Mapper, Difficulty: difficultyName(lb.Difficulty), DifficultyRaw: lb.DifficultyRaw,
			GameMode: lb.GameMode, CoverURL: lb.CoverURL, Status: lb.Status, Stars: lb.Stars,
		}
		if p.Legacy {
			out.Leaderboard.ID = lb.ID
		}
	}
	if s.ReplayState == model.ReplayArchived {
		out.Replay.Size, out.Replay.SHA256, out.Replay.ArchivedAt = s.ReplaySize, s.ReplaySHA256, s.ArchivedAt
		out.Replay.DownloadURL = base + reg.ReplayPath(ref)
		out.Replay.EmbedURL = base + reg.PlayPath("/embed", ref)
	}
	return out
}
