//go:build live

package beatleader_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/beatleader"
)

// Run manually: go test -tags live -run Live ./internal/beatleader
func TestLiveBeatLeader(t *testing.T) {
	c := beatleader.NewClient(beatleader.NewAPILimiter(), beatleader.NewCDNLimiter())
	ctx := context.Background()
	const livePlayerID = "76561198038925092" // instance owner's profile
	p, err := c.Player(ctx, livePlayerID)
	if err != nil || p.ID != livePlayerID || p.Name == "" {
		t.Fatalf("player: %+v %v", p, err)
	}
	page, err := c.Scores(ctx, p.ID, 1)
	if err != nil || len(page.Data) == 0 {
		t.Fatalf("scores: %v", err)
	}
	s := page.Data[0]
	if s.Leaderboard.Song.Hash == "" || s.Timeset.Time().IsZero() || !c.ReplayAllowed(s.Replay) {
		t.Fatalf("first score: %+v", s)
	}
	rc, err := c.Replay(ctx, s.Replay)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	defer rc.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(rc, head); err != nil || !bytes.Equal(head, []byte{0x69, 0x3d, 0x2d, 0x44}) {
		t.Fatalf("not a BSOR file: % x %v", head, err)
	}
}
