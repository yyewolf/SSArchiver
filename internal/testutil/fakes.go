package testutil

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

// T0 is the default fake "now" for service tests.
var T0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// Resolver is a fake service.Resolver.
type Resolver struct {
	mu      sync.Mutex
	Players map[string]scoresaber.Player
	Calls   int
}

func (r *Resolver) Player(_ context.Context, id string) (scoresaber.Player, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls++
	p, ok := r.Players[id]
	if !ok {
		return scoresaber.Player{}, fmt.Errorf("%w: player %s", scoresaber.ErrNotFound, id)
	}
	return p, nil
}

// Clock is a manually advanced clock.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func NewClock(t time.Time) *Clock { return &Clock{t: t} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// Gzip compresses s (for fake viewer bundles).
func Gzip(s string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return buf.Bytes()
}

// Item builds a ScoreSaber score item with sensible defaults.
func Item(playerID string, scoreID, lbID int64, setAt time.Time, hasReplay bool) scoresaber.ScoreItem {
	return scoresaber.ScoreItem{
		Score: scoresaber.Score{
			ID: scoreID, Rank: 1, ModifiedScore: 1_000_000, UnmodifiedScore: 1_000_000, Accuracy: 0.95,
			Mods: []string{}, MaxCombo: 500, FullCombo: true, HasReplay: hasReplay, PersonalBest: true,
			CreatedAt: setAt, Player: scoresaber.Player{ID: playerID, Name: "Player " + playerID},
			Device: scoresaber.Device{HMD: "Quest 3"},
		},
		Leaderboard: scoresaber.Leaderboard{
			ID: lbID,
			Map: scoresaber.Map{
				Hash: fmt.Sprintf("HASH%d", lbID), SongName: fmt.Sprintf("Song %d", lbID),
				SongAuthorName: "Artist", LevelAuthorName: "Mapper",
				CoverURL: "https://cdn.scoresaber.com/covers/x.png",
			},
			Difficulty: scoresaber.Difficulty{Difficulty: 9, GameMode: "SoloStandard", RawDifficulty: "_ExpertPlus_SoloStandard"},
			MaxScore:   1_100_000,
			Realm:      scoresaber.Realm{LeaderboardStatus: "UNRANKED"},
		},
	}
}
