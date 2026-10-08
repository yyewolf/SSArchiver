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
	p, err := c.Player(ctx, "1922350521131465")
	if err != nil || p.Name == "" {
		t.Fatalf("player: %+v %v", p, err)
	}
	page, err := c.Scores(ctx, p.ID, 1)
	if err != nil || len(page.Data) == 0 {
		t.Fatalf("scores: %v", err)
	}
}
