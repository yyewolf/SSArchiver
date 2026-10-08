//go:build live

package scoresaber_test

import (
	"context"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

// Run manually: go test -tags live -run Live ./internal/scoresaber
func TestLiveScoreSaber(t *testing.T) {
	c := scoresaber.NewClient(scoresaber.NewLimiter(10))
	ctx := context.Background()
	const livePlayerID = "76561198038925092" // instance owner's profile
	p, err := c.Player(ctx, livePlayerID)
	if err != nil || p.Name == "" {
		t.Fatalf("player: %+v %v", p, err)
	}
	page, err := c.Scores(ctx, p.ID, 1)
	if err != nil || len(page.Data) == 0 {
		t.Fatalf("scores: %v", err)
	}
}
